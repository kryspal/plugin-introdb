package main

import (
	"context"
	_ "embed"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/kryspal/plugin-sdk/pluginv1"
)

const (
	pluginID = "introdb"

	keyAPIKey  = "api_key"
	keySubmit  = "submit"
	keyBaseURL = "base_url"

	defaultBaseURL = "https://api.introdb.app"
	// attributionText is the attribution the IntroDB terms recommend (English original, the same in every language); shown in the source line on the plugin page.
	attributionText = "Intro data provided by IntroDB (introdb.app)"
	attributionLink = "https://introdb.app"

	// minInterval is the minimum interval between two requests: self-throttling, one request at a time (terms: reasonable usage, no bulk scraping).
	minInterval = time.Second
)

var apiKeyRE = regexp.MustCompile(`^idb_[A-Za-z0-9_\-]{4,200}$`)

// versionFile is the single source of the plugin version: the manifest's version.
//
//go:embed VERSION
var versionFile string

func version() string { return strings.TrimSpace(versionFile) }

type plugin struct {
	pluginv1.UnimplementedPluginServer
	pluginv1.UnimplementedSegmentProviderServer

	mu      sync.Mutex
	apiKey  string
	submit  bool
	baseURL string
	api     *api // replaceable in tests
	cache   segmentCache
}

func newPlugin() *plugin {
	return &plugin{baseURL: defaultBaseURL, api: newAPI()}
}

// Describe declares segments.provider and segments.submit. The attribution goes in Manifest.attribution (the source line on the plugin page) and is not repeated in the description.
func (p *plugin) Describe(_ context.Context, req *pluginv1.DescribeRequest) (*pluginv1.Manifest, error) {
	t := messages.For(req.GetLanguage())
	return &pluginv1.Manifest{
		Id: pluginID, Name: "IntroDB", Description: t.T("desc"), Version: version(), ProtocolVersion: pluginv1.ProtocolVersion,
		Capabilities: []string{pluginv1.CapabilitySegmentsProvider, pluginv1.CapabilitySegmentsSubmit},
		ConfigGroups: []*pluginv1.ConfigGroup{{Key: "advanced", Title: t.T("groupAdvanced")}},
		ConfigFields: []*pluginv1.ConfigField{
			{Key: keyAPIKey, Type: pluginv1.ConfigFieldType_CONFIG_FIELD_TYPE_SECRET, Label: t.T("keyLabel"), Description: t.T("keyHelp"),
				Link: "https://introdb.app", Placeholder: "idb_…"},
			{Key: keySubmit, Type: pluginv1.ConfigFieldType_CONFIG_FIELD_TYPE_BOOL, DefaultValue: "false", Label: t.T("submitLabel"), Description: t.T("submitHelp")},
			{Key: keyBaseURL, Type: pluginv1.ConfigFieldType_CONFIG_FIELD_TYPE_STRING, Group: "advanced", Label: t.T("baseURLLabel"),
				Description: t.T("baseURLHelp"), Placeholder: defaultBaseURL},
		},
		Attribution: &pluginv1.Attribution{Text: attributionText, Link: attributionLink},
		// One request at a time: concurrency 1.
		SuggestedConcurrency: 1,
	}, nil
}

// Configure validates the settings: the key may be empty (query only); if given it must match the idb_ prefix format; turning on submitting requires a key; the URL must be https.
// It does not verify the key over the network (it is used only when submitting, and a rejection is reported then).
func (p *plugin) Configure(_ context.Context, req *pluginv1.ConfigureRequest) (*pluginv1.ConfigureResponse, error) {
	t := messages.For(req.GetLanguage())
	v := req.GetValues()
	resp := &pluginv1.ConfigureResponse{FieldErrors: map[string]string{}}
	key := strings.TrimSpace(v[keyAPIKey])
	if key != "" && !apiKeyRE.MatchString(key) {
		resp.FieldErrors[keyAPIKey] = t.T("errKeyFormat")
	}
	submit := v[keySubmit] == "true"
	if submit && key == "" {
		resp.FieldErrors[keySubmit] = t.T("errSubmitNoKey")
	}
	base := strings.TrimSpace(v[keyBaseURL])
	if base == "" {
		base = defaultBaseURL
	}
	if u, err := url.Parse(base); err != nil || u.Scheme != "https" || u.Host == "" {
		resp.FieldErrors[keyBaseURL] = t.T("errHTTPS")
	}
	if len(resp.FieldErrors) > 0 {
		return resp, nil
	}
	p.mu.Lock()
	p.apiKey, p.submit, p.baseURL = key, submit, strings.TrimRight(base, "/")
	p.mu.Unlock()
	return resp, nil
}

func (p *plugin) settings() (key string, submit bool, base string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.apiKey, p.submit, p.baseURL
}
