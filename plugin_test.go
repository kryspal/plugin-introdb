package main

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/kryspal/plugin-sdk"
	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
)

var han = regexp.MustCompile(`\p{Han}`)

func TestDescribe(t *testing.T) {
	p := newPlugin()
	for _, lang := range []string{"en", "zh-TW", "zh-CN", "", "ja"} {
		m, err := p.Describe(context.Background(), &pluginv1.DescribeRequest{Language: lang})
		if err != nil {
			t.Fatal(err)
		}
		if m.GetId() != "introdb" || m.GetName() != "IntroDB" || m.GetDescription() == "" || m.GetVersion() != version() ||
			!slices.Equal(m.GetCapabilities(), []string{pluginv1.CapabilitySegmentsProvider, pluginv1.CapabilitySegmentsSubmit}) ||
			len(m.GetFeatures()) != 0 || m.GetSuggestedConcurrency() != 1 || len(m.GetConfigFields()) != 3 {
			t.Errorf("Describe(%q) = %v", lang, m)
		}
		// Attribution: the original text the IntroDB terms recommend, the same in every language, placed in the source line (on the plugin page), not repeated in the description.
		if m.GetAttribution().GetText() != "Intro data provided by IntroDB (introdb.app)" || m.GetAttribution().GetLink() != "https://introdb.app" ||
			strings.Contains(m.GetDescription(), "Intro data provided by") {
			t.Errorf("Describe(%q) attribution = %v, description %q", lang, m.GetAttribution(), m.GetDescription())
		}
		if (lang == "en" || lang == "" || lang == "ja") && han.MatchString(m.GetDescription()) {
			t.Errorf("Describe(%q): Han characters in the English text", lang)
		}
		for _, f := range m.GetConfigFields() {
			if f.GetLabel() == "" || f.GetKey() != keyBaseURL && f.GetDescription() == "" {
				t.Errorf("Describe(%q): field %s has no label or help", lang, f.GetKey())
			}
		}
	}
	for _, tag := range messages.Tags() {
		for key := range messageTables["en"] {
			if messages.For(tag).T(key) == "" {
				t.Errorf("%s: missing %s", tag, key)
			}
		}
	}
	if strings.ContainsAny(messageTables["zh-TW"]["desc"]+messageTables["zh-TW"]["submitHelp"], "么识断读网为") {
		t.Error("the zh-TW text has simplified characters")
	}
	if strings.ContainsAny(messageTables["zh-CN"]["desc"]+messageTables["zh-CN"]["submitHelp"], "麼識斷讀網為") {
		t.Error("the zh-CN text has traditional characters")
	}
}

func TestConfigure(t *testing.T) {
	p := newPlugin()
	for name, c := range map[string]struct {
		values map[string]string
		field  string // the field expected to error; empty means it passes
	}{
		"empty (lookups only)": {map[string]string{}, ""},
		"valid key":            {map[string]string{keyAPIKey: "idb_abcdef12"}, ""},
		"bad key format":       {map[string]string{keyAPIKey: "abcdef"}, keyAPIKey},
		"uploads need a key":   {map[string]string{keySubmit: "true"}, keySubmit},
		"uploads with a key":   {map[string]string{keySubmit: "true", keyAPIKey: "idb_abcdef12"}, ""},
		"http base url":        {map[string]string{keyBaseURL: "http://proxy.example"}, keyBaseURL},
		"https base url":       {map[string]string{keyBaseURL: "https://proxy.example/"}, ""},
		"not a url":            {map[string]string{keyBaseURL: "::"}, keyBaseURL},
	} {
		resp, err := p.Configure(context.Background(), &pluginv1.ConfigureRequest{Values: c.values})
		if err != nil {
			t.Fatal(err)
		}
		if c.field == "" && len(resp.GetFieldErrors()) != 0 || c.field != "" && resp.GetFieldErrors()[c.field] == "" {
			t.Errorf("%s: field errors = %v, want %q", name, resp.GetFieldErrors(), c.field)
		}
	}
	// Once it passes the settings take effect (the trailing slash is stripped).
	_, _ = p.Configure(context.Background(), &pluginv1.ConfigureRequest{Values: map[string]string{keySubmit: "true", keyAPIKey: "idb_abcdef12", keyBaseURL: "https://proxy.example/"}})
	if k, s, b := p.settings(); k != "idb_abcdef12" || !s || b != "https://proxy.example" {
		t.Errorf("settings = %q %v %q", k, s, b)
	}
}

// TestPluginProcess is the conformance check of the plugin process (the host's way): it compiles the executable and starts it with the host's environment variables,
// checking the handshake, health check, Describe, Configure with empty settings, GetSegments (no IMDb ID returns an empty list, no network) and graceful exit after SIGTERM.
func TestPluginProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the plugin binary")
	}
	exe := filepath.Join(t.TempDir(), "introdb")
	if out, err := exec.Command("go", "build", "-o", exe, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	sockDir, err := os.MkdirTemp("", "idb-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(sockDir) }()
	cmd := exec.Command(exe)
	cmd.Env = []string{pluginapi.EnvCookie + "=" + pluginapi.CookieValue, pluginapi.EnvProtocols + "=1", pluginapi.EnvSocketDir + "=" + sockDir, "HOME=" + t.TempDir()}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	hs, err := pluginapi.ParseHandshake(strings.TrimSpace(line))
	if err != nil || hs.Protocol != 1 || hs.Network != pluginapi.NetworkUnix {
		t.Fatalf("handshake %q: %+v, %v", line, hs, err)
	}
	conn, err := grpc.NewClient("unix:"+hs.Address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	hc := healthpb.NewHealthClient(conn)
	for _, svc := range []string{"", pluginv1.HealthServiceSegmentProvider} {
		if r, err := hc.Check(ctx, &healthpb.HealthCheckRequest{Service: svc}); err != nil || r.GetStatus() != healthpb.HealthCheckResponse_SERVING {
			t.Errorf("health %q = %v, %v", svc, r, err)
		}
	}
	pc := pluginv1.NewPluginClient(conn)
	m, err := pc.Describe(ctx, &pluginv1.DescribeRequest{Language: "zh-TW"})
	if err != nil || m.GetId() != "introdb" || m.GetProtocolVersion() != pluginv1.ProtocolVersion {
		t.Fatalf("Describe = %v, %v", m, err)
	}
	if resp, err := pc.Configure(ctx, &pluginv1.ConfigureRequest{}); err != nil || len(resp.GetFieldErrors()) != 0 {
		t.Errorf("Configure with no settings = %v, %v", resp, err)
	}
	sc := pluginv1.NewSegmentProviderClient(conn)
	resp, err := sc.GetSegments(ctx, &pluginv1.GetSegmentsRequest{Target: &pluginv1.SegmentTarget{Type: pluginv1.ItemType_ITEM_TYPE_MOVIE}})
	if err != nil || len(resp.GetCandidates()) != 0 {
		t.Errorf("GetSegments without an IMDb id = %v, %v", resp, err)
	}
	if _, err := pluginv1.NewMetadataProviderClient(conn).Search(ctx, &pluginv1.SearchRequest{}); err == nil {
		t.Error("the metadata service must not be registered")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	stopped = true
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("exit after SIGTERM: %v", err)
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Error("the plugin did not exit after SIGTERM")
	}
}
