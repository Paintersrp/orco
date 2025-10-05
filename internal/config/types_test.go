package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidatePortSuccess(t *testing.T) {
	cases := []string{
		"8080:8080",
		"127.0.0.1:9090:8080",
		"[2001:db8::1]:8080:8080",
		"0.0.0.0:8443:443/tcp",
		"8080-8081:8080-8081/udp",
	}
	for _, spec := range cases {
		spec := spec
		t.Run(spec, func(t *testing.T) {
			if err := validatePort(spec); err != nil {
				t.Fatalf("validatePort(%q) returned error: %v", spec, err)
			}
		})
	}
}

func TestValidatePortFailures(t *testing.T) {
	cases := []struct {
		name string
		spec string
		want string
	}{
		{name: "missing host port", spec: "8080", want: "host port must be specified"},
		{name: "missing container port", spec: "8080:", want: "No port specified"},
		{name: "invalid host ip", spec: "localhost:8080:80", want: "Invalid ip address"},
		{name: "invalid proto", spec: "127.0.0.1:8080:80/foo", want: "Invalid proto"},
		{name: "host port zero", spec: "0:8080", want: "host port must be in range"},
		{name: "container port zero", spec: "8080:0", want: "container port must be in range"},
		{name: "host ip without port", spec: "127.0.0.1::8080", want: "host port must be specified"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := validatePort(tc.spec)
			if err == nil {
				t.Fatalf("validatePort(%q) returned nil error", tc.spec)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unexpected error for %q: got %q want substring %q", tc.spec, err, tc.want)
			}
		})
	}
}

func TestValidateVolumeSpecSuccess(t *testing.T) {
	dir := t.TempDir()
	spec := filepath.Join(dir, "data") + ":/var/lib/data"
	if err := validateVolumeSpec(spec); err != nil {
		t.Fatalf("validateVolumeSpec returned error: %v", err)
	}

	spec = filepath.Join(dir, "cache") + ":/cache:ro"
	if err := validateVolumeSpec(spec); err != nil {
		t.Fatalf("validateVolumeSpec with mode returned error: %v", err)
	}
}

func TestValidateVolumeSpecFailures(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		spec string
		want string
	}{
		{name: "empty", spec: "", want: "volume specification is empty"},
		{name: "format", spec: "/host", want: "expected format"},
		{name: "host relative", spec: "data:/var/lib/data", want: "host path"},
		{name: "container missing", spec: "/host:", want: "container path is required"},
		{name: "container relative", spec: filepath.Join(dir, "data") + ":data", want: "container path"},
		{name: "mode empty", spec: filepath.Join(dir, "data") + ":/var/data:", want: "mode is empty"},
		{name: "mode extra", spec: filepath.Join(dir, "data") + ":/var/data:ro:rw", want: "unexpected ':'"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := validateVolumeSpec(tc.spec)
			if err == nil {
				t.Fatalf("validateVolumeSpec(%q) returned nil error", tc.spec)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unexpected error for %q: got %q want substring %q", tc.spec, err, tc.want)
			}
		})
	}
}
