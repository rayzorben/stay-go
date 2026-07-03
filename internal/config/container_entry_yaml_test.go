package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// TestContainerListComposeForm verifies the docker-compose-style mapping form
// of containers:, plus the compose field aliases and flexible value types.
func TestContainerListComposeForm(t *testing.T) {
	const src = `
containers:
  frigate:
    container_name: frigate
    image: ghcr.io/blakeblackshear/frigate:stable
    privileged: true
    restart: unless-stopped
    stop_grace_period: 30s
    shm_size: "512mb"
    devices:
      - /dev/dri:/dev/dri
    ports:
      - "8555:8555/udp"
    volumes:
      - /storage/frigate/config:/config
      - type: tmpfs
        target: /tmp/cache
        tmpfs:
          size: 1000000000
    environment:
      FRIGATE_RTSP_PASSWORD: "pw"
      MQTT_PASSWORD: 1234
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(cfg.Containers) != 1 {
		t.Fatalf("want 1 container, got %d", len(cfg.Containers))
	}
	c := cfg.Containers[0]
	if c.Name != "frigate" {
		t.Errorf("name: got %q", c.Name)
	}
	if c.ContainerName() != "frigate" {
		t.Errorf("ContainerName(): got %q", c.ContainerName())
	}
	if c.StopTimeout != "30s" || c.ShmSize != "512mb" {
		t.Errorf("stop_grace_period/shm_size: got %q / %q", c.StopTimeout, c.ShmSize)
	}
	if len(c.Volumes) != 2 {
		t.Fatalf("want 2 volumes, got %d", len(c.Volumes))
	}
	if c.Volumes[0].Short != "/storage/frigate/config:/config" {
		t.Errorf("short volume: got %+v", c.Volumes[0])
	}
	if c.Volumes[1].Type != "tmpfs" || c.Volumes[1].Target != "/tmp/cache" || c.Volumes[1].TmpfsSize != "1000000000" {
		t.Errorf("tmpfs volume: got %+v", c.Volumes[1])
	}
	want := map[string]bool{"FRIGATE_RTSP_PASSWORD=pw": true, "MQTT_PASSWORD=1234": true}
	if len(c.Environment) != 2 {
		t.Fatalf("want 2 env vars, got %v", c.Environment)
	}
	for _, e := range c.Environment {
		if !want[e] {
			t.Errorf("unexpected env entry %q", e)
		}
	}
}

// TestContainerListSequenceForm verifies the original sequence form still works.
func TestContainerListSequenceForm(t *testing.T) {
	const src = `
containers:
  - name: portainer
    image: portainer/portainer-ce:latest
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    environment:
      - TZ=UTC
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(cfg.Containers) != 1 || cfg.Containers[0].Name != "portainer" {
		t.Fatalf("got %+v", cfg.Containers)
	}
	if len(cfg.Containers[0].Volumes) != 1 || cfg.Containers[0].Volumes[0].Short != "/var/run/docker.sock:/var/run/docker.sock" {
		t.Errorf("volumes: %+v", cfg.Containers[0].Volumes)
	}
	if len(cfg.Containers[0].Environment) != 1 || cfg.Containers[0].Environment[0] != "TZ=UTC" {
		t.Errorf("environment: %+v", cfg.Containers[0].Environment)
	}
}
