package main

import (
	"os"
	"testing"
)

// TestLoadConfigCompilesRegexes verifies that LoadConfig pre-compiles the
// regex for every rule in both CacheRules and ServiceNameDefaultCacheTTL so
// the hot request path can match without recompiling on every call.
func TestLoadConfigCompilesRegexes(t *testing.T) {
	yaml := `
cache_folder: /tmp
cache_folder_https: /tmp
default_cache_ttl: 1h
caching_rules:
  rpm-rule:
    regex: '.*\.rpm$'
    ttl: 24h
  deb-rule:
    regex: '.*\.deb$'
    ttl: 12h
service_default_cache_ttl:
  myservice:
    regex: 'myservice\.'
    ttl: 48h
`
	f, err := os.CreateTemp("", "pkgproxy-cfg-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(yaml); err != nil {
		t.Fatal(err)
	}
	f.Close()

	cfg, err := LoadConfig(f.Name())
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	for name, cr := range cfg.CacheRules {
		if cr.CompiledRegex == nil {
			t.Errorf("CacheRules[%q].CompiledRegex is nil; LoadConfig must pre-compile it", name)
			continue
		}
		// Spot-check that the compiled regex matches what the Regex field says it should.
		switch name {
		case "rpm-rule":
			if !cr.CompiledRegex.MatchString("package.rpm") {
				t.Errorf("CacheRules[%q].CompiledRegex should match .rpm files", name)
			}
			if cr.CompiledRegex.MatchString("package.deb") {
				t.Errorf("CacheRules[%q].CompiledRegex should not match .deb files", name)
			}
		case "deb-rule":
			if !cr.CompiledRegex.MatchString("package.deb") {
				t.Errorf("CacheRules[%q].CompiledRegex should match .deb files", name)
			}
		}
	}

	for name, cr := range cfg.ServiceNameDefaultCacheTTL {
		if cr.CompiledRegex == nil {
			t.Errorf("ServiceNameDefaultCacheTTL[%q].CompiledRegex is nil; LoadConfig must pre-compile it", name)
		}
	}
}
