<script>
  let {
    points = [],
    width = 240,
    height = 60,
    color = 'var(--accent)',
    fillOpacity = 0.15,
    strokeWidth = 1.5,
    yMax = null,
  } = $props();

  // Compute path from points. The X axis spans the full width (most-recent
  // point on the right). The Y axis is auto-scaled: 0 at bottom, max at
  // top. If `yMax` is provided, use it instead of the data max (useful
  // for % metrics that should cap at 100).
  const path = $derived.by(() => {
    if (!Array.isArray(points) || points.length === 0) return { d: '', fill: '', yMax: 1, yMin: 0 };
    const xs = points.map((_, i) => i);
    const ys = points.map((p) => p.v);
    let lo = Math.min(...ys);
    let hi = Math.max(...ys);
    if (lo > 0) lo = 0;
    if (yMax != null) hi = yMax;
    if (hi <= lo) hi = lo + 1;
    const stepX = xs.length > 1 ? width / (xs.length - 1) : 0;
    const mapY = (v) => height - ((v - lo) / (hi - lo)) * height;
    let d = '';
    for (let i = 0; i < points.length; i++) {
      const x = i * stepX;
      const y = mapY(points[i].v);
      d += (i === 0 ? 'M' : 'L') + x.toFixed(1) + ' ' + y.toFixed(1) + ' ';
    }
    const fill = d + 'L' + width + ' ' + height + 'L0 ' + height + 'Z';
    return { d, fill, yMax: hi, yMin: lo };
  });
</script>

<svg
  viewBox="0 0 {width} {height}"
  width="100%"
  {height}
  preserveAspectRatio="none"
  class="overflow-visible"
>
  {#if path.d}
    <path d={path.fill} fill={color} fill-opacity={fillOpacity} stroke="none" />
    <path
      d={path.d}
      fill="none"
      stroke={color}
      stroke-width={strokeWidth}
      stroke-linejoin="round"
      stroke-linecap="round"
    />
  {/if}
</svg>
