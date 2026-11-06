package libvirt

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// 3D acceleration (virgl) support.
//
// A plain <model type='virtio'/> gives the guest a paravirtual 2D display:
// enough for any X11 desktop, not enough for Wayland compositors that
// require a GL context (niri, sway, Hyprland without its software
// fallback). Virtio-GPU exposes virgl for that, but only when the host
// QEMU was built with the GL modules AND libvirt is given an
// <acceleration accel3d='yes'/> on the model plus an egl-headless
// renderer bound to a DRM render node.
//
// Both halves are required: the acceleration tag alone leaves the guest
// with a virgl-capable device and no host renderer behind it.

// renderNodePath returns the first /dev/dri/renderD* node, or "" when the
// host has none (no GPU, or only a display card without offscreen
// acceleration). Sorted so the choice is deterministic across reboots.
func renderNodePath() string {
	entries, err := os.ReadDir("/dev/dri")
	if err != nil {
		return ""
	}
	var nodes []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "renderD") {
			nodes = append(nodes, filepath.Join("/dev/dri", e.Name()))
		}
	}
	if len(nodes) == 0 {
		return ""
	}
	sort.Strings(nodes)
	return nodes[0]
}

// buildVideoXML renders the <video> block, optionally with 3D acceleration.
func buildVideoXML(model string, accel3d bool) string {
	if model == "none" || model == "" {
		return ""
	}
	if accel3d {
		return fmt.Sprintf(
			"<video><model type='%s' heads='1' primary='yes'><acceleration accel3d='yes'/></model></video>",
			xmlEscape(model),
		)
	}
	return fmt.Sprintf("<video><model type='%s' heads='1' vram='16384'/></video>", xmlEscape(model))
}

// eglHeadlessXML is the host-side renderer virgl draws into. It coexists
// with the VNC/SPICE graphics devices (which still serve the browser
// console), so the guest keeps a working remote console either way.
func eglHeadlessXML(renderNode string) string {
	if renderNode == "" {
		return ""
	}
	return fmt.Sprintf(`<graphics type='egl-headless'>
      <gl rendernode='%s'/>
    </graphics>`, xmlEscape(renderNode))
}

// vncSpiceGraphicsRE matches only the VNC/SPICE graphics blocks. An earlier
// version matched every <graphics>, which silently deleted the
// egl-headless renderer whenever the user changed the console type from
// the UI — 3D accel stopped working with no visible reason.
//
// Self-closing and paired forms are matched separately: a body-matching
// pattern applied to `<graphics type='vnc' .../>` would run past the tag
// (there is no </graphics> for it) and swallow the next block's closing
// tag, taking the egl-headless renderer with it.
var vncSpiceGraphicsSelfClosingRE = regexp.MustCompile(`<graphics\b[^>]*type='(?:vnc|spice)'[^>]*/>\s*`)
var vncSpiceGraphicsPairedRE = regexp.MustCompile(`<graphics\b[^>]*type='(?:vnc|spice)'[^>]*>[\s\S]*?</graphics>\s*`)

// stripVNCSpiceGraphics removes the console graphics devices, leaving any
// egl-headless renderer in place.
func stripVNCSpiceGraphics(xmlDesc string) string {
	xmlDesc = vncSpiceGraphicsSelfClosingRE.ReplaceAllString(xmlDesc, "")
	return vncSpiceGraphicsPairedRE.ReplaceAllString(xmlDesc, "")
}

var eglHeadlessSelfClosingRE = regexp.MustCompile(`<graphics\b[^>]*type='egl-headless'[^>]*/>\s*`)
var eglHeadlessPairedRE = regexp.MustCompile(`<graphics\b[^>]*type='egl-headless'[^>]*>[\s\S]*?</graphics>\s*`)

func stripEGLHeadless(xmlDesc string) string {
	xmlDesc = eglHeadlessSelfClosingRE.ReplaceAllString(xmlDesc, "")
	return eglHeadlessPairedRE.ReplaceAllString(xmlDesc, "")
}

// extractVideoModel returns the model type of the first <video> device, or
// "" when the domain has none.
func extractVideoModel(xmlDesc string) string {
	re := regexp.MustCompile(`<video>[\s\S]*?<model\b[^>]*type='([^']+)'`)
	if m := re.FindStringSubmatch(xmlDesc); len(m) == 2 {
		return m[1]
	}
	return ""
}

// hasEGLHeadless reports whether the domain already has a 3D renderer.
func hasEGLHeadless(xmlDesc string) bool {
	return strings.Contains(xmlDesc, "type='egl-headless'") || strings.Contains(xmlDesc, `type="egl-headless"`)
}

// hasAccel3D reports whether the video model carries accel3d='yes'.
func hasAccel3D(xmlDesc string) bool {
	return strings.Contains(xmlDesc, "accel3d='yes'") || strings.Contains(xmlDesc, `accel3d="yes"`)
}

var accel3DTagRE = regexp.MustCompile(`<acceleration\b[^>]*accel3d='yes'[^>]*/>\s*`)

// setAccel3DInVideoXML adds or removes the <acceleration> child of the
// video model. Returns the XML unchanged when the requested state already
// holds, so a no-op save does not churn the definition.
func setAccel3DInVideoXML(xmlDesc string, on bool) string {
	if on {
		if hasAccel3D(xmlDesc) {
			return xmlDesc
		}
		// Self-closing <model .../> must become a container element.
		re := regexp.MustCompile(`(<video>[\s\S]*?<model\b[^>]*?)(\s*/>)`)
		if re.MatchString(xmlDesc) {
			return re.ReplaceAllString(xmlDesc, "${1}><acceleration accel3d='yes'/></model>")
		}
		// Already a container: append before its close tag.
		re2 := regexp.MustCompile(`(<model\b[^>]*>)([\s\S]*?)(</model>)`)
		return re2.ReplaceAllString(xmlDesc, "${1}${2}<acceleration accel3d='yes'/>${3}")
	}
	return accel3DTagRE.ReplaceAllString(xmlDesc, "")
}

// applyAccel3D reconciles the video model, the acceleration tag and the
// egl-headless renderer in one place, so create and update cannot drift.
// It returns an error when acceleration is requested on a non-virtio model
// (virgl is a virtio-gpu feature) or when the host has no render node.
func applyAccel3D(xmlDesc string, on bool, videoModel string) (string, error) {
	if on {
		if videoModel != "virtio" {
			return xmlDesc, fmt.Errorf("3D acceleration requires the virtio video model (got %q)", videoModel)
		}
		node := renderNodePath()
		if node == "" {
			return xmlDesc, fmt.Errorf("3D acceleration requires a DRM render node (none under /dev/dri)")
		}
		xmlDesc = setAccel3DInVideoXML(xmlDesc, true)
		if !hasEGLHeadless(xmlDesc) {
			xmlDesc = strings.Replace(xmlDesc, "</devices>", "    "+eglHeadlessXML(node)+"\n  </devices>", 1)
		}
		return xmlDesc, nil
	}

	xmlDesc = setAccel3DInVideoXML(xmlDesc, false)
	xmlDesc = stripEGLHeadless(xmlDesc)
	return xmlDesc, nil
}
