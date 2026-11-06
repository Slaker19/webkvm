package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"webkvm/internal/models"

	"github.com/docker/go-sdk/client"
	mobyclient "github.com/moby/moby/client"
)

// dockerAPIClient defines the subset of Docker client operations we use.
// This allows mocking in tests.
type dockerAPIClient interface {
	ContainerList(ctx context.Context, options mobyclient.ContainerListOptions) (mobyclient.ContainerListResult, error)
	ContainerStats(ctx context.Context, containerID string, options mobyclient.ContainerStatsOptions) (mobyclient.ContainerStatsResult, error)
	ImageList(ctx context.Context, options mobyclient.ImageListOptions) (mobyclient.ImageListResult, error)
	VolumeList(ctx context.Context, options mobyclient.VolumeListOptions) (mobyclient.VolumeListResult, error)
	NetworkList(ctx context.Context, options mobyclient.NetworkListOptions) (mobyclient.NetworkListResult, error)
	Ping(ctx context.Context, options mobyclient.PingOptions) (mobyclient.PingResult, error)
	ServerVersion(ctx context.Context, options mobyclient.ServerVersionOptions) (mobyclient.ServerVersionResult, error)
	Close() error
}

// dockerClientWrapper wraps the Docker client with connection state tracking.
type dockerClientWrapper struct {
	client    dockerAPIClient
	available bool
	version   string
	compose   bool
}

// dockerClientFactory creates a dockerAPIClient. Can be overridden in tests.
// The default factory checks for socket existence before creating the client.
var dockerClientFactory = func(ctx context.Context) (dockerAPIClient, error) {
	// Check if docker socket exists
	socketPath := "/var/run/docker.sock"
	if runtime.GOOS != "windows" {
		if _, err := os.Stat(socketPath); err != nil {
			return nil, err
		}
	}
	// Use the new docker go-sdk
	return client.New(ctx)
}

var (
	dockerClient     *dockerClientWrapper
	dockerClientOnce sync.Once
)

// initDockerClient initializes the Docker client lazily.
// Returns nil if Docker is not available.
func initDockerClient(ctx context.Context) *dockerClientWrapper {
	dockerClientOnce.Do(func() {
		// Create Docker client using factory (allows test override)
		cli, err := dockerClientFactory(ctx)
		if err != nil {
			return
		}

		// Test connection with a short timeout
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		_, err = cli.Ping(pingCtx, mobyclient.PingOptions{})
		if err != nil {
			cli.Close()
			return
		}

		// Get server version
		versionCtx, versionCancel := context.WithTimeout(ctx, 2*time.Second)
		defer versionCancel()

		serverVersion, err := cli.ServerVersion(versionCtx, mobyclient.ServerVersionOptions{})
		if err != nil {
			cli.Close()
			return
		}

		wrapper := &dockerClientWrapper{
			client:    cli,
			available: true,
			version:   serverVersion.Version,
		}

		// Check for docker compose plugin
		for _, path := range []string{
			"/usr/libexec/docker/cli-plugins/docker-compose",
			"/usr/lib/docker/cli-plugins/docker-compose",
			"/usr/local/lib/docker/cli-plugins/docker-compose",
			os.Getenv("HOME") + "/.docker/cli-plugins/docker-compose",
		} {
			if _, err := os.Stat(path); err == nil {
				wrapper.compose = true
				break
			}
		}

		dockerClient = wrapper
	})

	return dockerClient
}

// collectDockerInfoSDK gathers Docker information using the Go SDK.
// This is much faster than running multiple CLI commands.
func (m *HostMonitor) collectDockerInfoSDK(ctx context.Context) *models.DockerInfo {
	wrapper := initDockerClient(ctx)
	if wrapper == nil || !wrapper.available {
		return nil
	}

	docker := &models.DockerInfo{
		Available:        true,
		Version:          wrapper.version,
		ComposeAvailable: wrapper.compose,
	}

	// Use a shorter timeout for individual operations
	opCtx, opCancel := context.WithTimeout(ctx, 5*time.Second)
	defer opCancel()

	// Collect containers, images, volumes, networks in parallel
	var wg sync.WaitGroup
	var mu sync.Mutex

	// Fetch containers
	wg.Add(1)
	go func() {
		defer wg.Done()
		result, err := wrapper.client.ContainerList(opCtx, mobyclient.ContainerListOptions{All: true})
		if err != nil {
			return
		}

		cont := make([]models.DockerContainer, 0, len(result.Items))
		for _, c := range result.Items {
			name := ""
			if len(c.Names) > 0 {
				name = strings.TrimPrefix(c.Names[0], "/")
			}
			cont = append(cont, models.DockerContainer{
				ID:     c.ID[:12],
				Name:   name,
				Image:  c.Image,
				State:  string(c.State),
				Status: c.Status,
			})
		}

		mu.Lock()
		docker.Containers = cont
		mu.Unlock()
	}()

	// Fetch images
	wg.Add(1)
	go func() {
		defer wg.Done()
		result, err := wrapper.client.ImageList(opCtx, mobyclient.ImageListOptions{All: false})
		if err != nil {
			return
		}

		imgs := make([]models.DockerImage, 0, len(result.Items))
		for i, img := range result.Items {
			if i >= maxDockerImages {
				break
			}

			repo := "<none>"
			tag := "<none>"
			if len(img.RepoTags) > 0 {
				parts := strings.SplitN(img.RepoTags[0], ":", 2)
				repo = parts[0]
				if len(parts) > 1 {
					tag = parts[1]
				}
			}

			// Get full ID without sha256: prefix, truncated to 12 chars
			imgID := strings.TrimPrefix(img.ID, "sha256:")
			if len(imgID) > 12 {
				imgID = imgID[:12]
			}

			imgs = append(imgs, models.DockerImage{
				ID:           imgID,
				Repository:   repo,
				Tag:          tag,
				Size:         img.Size,
				CreatedAt:    time.Unix(img.Created, 0).Format("2006-01-02 15:04:05"),
				CreatedSince: formatTimeAgo(time.Unix(img.Created, 0)),
			})
		}

		mu.Lock()
		docker.Images = imgs
		docker.ImagesCount = len(result.Items)
		mu.Unlock()
	}()

	// Fetch volume count
	wg.Add(1)
	go func() {
		defer wg.Done()
		result, err := wrapper.client.VolumeList(opCtx, mobyclient.VolumeListOptions{})
		if err != nil {
			return
		}
		mu.Lock()
		docker.VolumesCount = len(result.Items)
		mu.Unlock()
	}()

	// Fetch network count
	wg.Add(1)
	go func() {
		defer wg.Done()
		result, err := wrapper.client.NetworkList(opCtx, mobyclient.NetworkListOptions{})
		if err != nil {
			return
		}
		mu.Lock()
		docker.NetworksCount = len(result.Items)
		mu.Unlock()
	}()

	wg.Wait()

	// Fetch container stats for running containers
	m.enrichContainerStats(opCtx, wrapper, docker)

	return docker
}

// containerStats represents the stats response from Docker API.
type containerStats struct {
	CPUStats    cpuStats    `json:"cpu_stats"`
	PreCPUStats cpuStats    `json:"precpu_stats"`
	MemoryStats memoryStats `json:"memory_stats"`
}

type cpuStats struct {
	CPUUsage struct {
		TotalUsage        uint64   `json:"total_usage"`
		PercpuUsage       []uint64 `json:"percpu_usage"`
		UsageInKernelmode uint64   `json:"usage_in_kernelmode"`
		UsageInUsermode   uint64   `json:"usage_in_usermode"`
	} `json:"cpu_usage"`
	SystemUsage uint64 `json:"system_cpu_usage"`
	OnlineCPUs  uint32 `json:"online_cpus"`
}

type memoryStats struct {
	Usage    uint64 `json:"usage"`
	Limit    uint64 `json:"limit"`
	Cache    uint64 `json:"cache"`
	RSS      uint64 `json:"rss"`
	MaxUsage uint64 `json:"max_usage"`
}

// enrichContainerStats fetches CPU and memory stats for running containers.
func (m *HostMonitor) enrichContainerStats(ctx context.Context, wrapper *dockerClientWrapper, docker *models.DockerInfo) {
	// Find running containers
	var runningContainers []string
	containerNameMap := make(map[string]int) // ID -> index in docker.Containers

	for i, c := range docker.Containers {
		if c.State == "running" {
			runningContainers = append(runningContainers, c.ID)
			containerNameMap[c.ID] = i
		}
	}

	if len(runningContainers) == 0 {
		return
	}

	// Fetch stats for each running container in parallel
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, containerID := range runningContainers {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()

			statsCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()

			// Use ContainerStats with streaming disabled for one-shot stats
			resp, err := wrapper.client.ContainerStats(statsCtx, id, mobyclient.ContainerStatsOptions{
				Stream:                false,
				IncludePreviousSample: true,
			})
			if err != nil {
				return
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return
			}

			var stats containerStats
			if err := json.Unmarshal(body, &stats); err != nil {
				return
			}

			// Calculate CPU percentage
			cpuDelta := stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage
			systemDelta := stats.CPUStats.SystemUsage - stats.PreCPUStats.SystemUsage
			var cpuPerc float64
			if systemDelta > 0 && cpuDelta > 0 {
				numCPUs := float64(stats.CPUStats.OnlineCPUs)
				if numCPUs == 0 {
					numCPUs = float64(len(stats.CPUStats.CPUUsage.PercpuUsage))
				}
				cpuPerc = (float64(cpuDelta) / float64(systemDelta)) * numCPUs * 100.0
			}

			// Format memory usage
			memUsage := formatBytes(stats.MemoryStats.Usage) + " / " + formatBytes(stats.MemoryStats.Limit)

			mu.Lock()
			if idx, ok := containerNameMap[id]; ok {
				docker.Containers[idx].CPUPerc = cpuPerc
				docker.Containers[idx].MemUsage = memUsage
			}
			mu.Unlock()
		}(containerID)
	}

	wg.Wait()
}

// formatTimeAgo formats a time as a human-readable "time ago" string.
func formatTimeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "Just now"
	case d < time.Hour:
		mins := int(d.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	case d < 24*time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	case d < 30*24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	case d < 365*24*time.Hour:
		months := int(d.Hours() / 24 / 30)
		if months == 1 {
			return "1 month ago"
		}
		return fmt.Sprintf("%d months ago", months)
	default:
		years := int(d.Hours() / 24 / 365)
		if years == 1 {
			return "1 year ago"
		}
		return fmt.Sprintf("%d years ago", years)
	}
}

// formatBytes formats bytes as a human-readable string.
func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// closeDockerClient closes the Docker client connection.
// Should be called on application shutdown.
func closeDockerClient() {
	if dockerClient != nil && dockerClient.client != nil {
		dockerClient.client.Close()
	}
}

// resetDockerClient resets the cached client. Used in tests only.
func resetDockerClient() {
	dockerClientOnce = sync.Once{}
	dockerClient = nil
}

// isDockerAvailable returns true if Docker daemon is accessible.
func isDockerAvailable(ctx context.Context) bool {
	wrapper := initDockerClient(ctx)
	return wrapper != nil && wrapper.available
}
