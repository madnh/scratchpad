package config

import (
	"slices"
	"sync"
	"testing"
)

func TestMergeHotKeepsColdGroups(t *testing.T) {
	running := Config{
		DisplayName: "Old", DefaultProject: "old", Instance: "inst-a", Dir: "/somewhere",
		Limits: Limits{MaxSectionsPerPad: 10},
		Wait:   Wait{DefaultS: 5, MaxS: 10},
		Rules:  RulesPolicy{Store: RulesWriteUI, Project: RulesWriteUI, Pad: RulesWriteOpener},
		TCP:    TCP{Port: 1111, TokenDigests: []string{"sha256:aaa"}},
		UI:     UI{Port: 2222},
		// Derived at load time and bound by the running process — swapping them under it
		// would leave every path in memory pointing somewhere the process is not serving.
		RootDir: "/run/root", ProjectsDir: "/run/root/projects", SocketPath: "/run/root/inst-a.sock",
	}
	fresh := Config{
		DisplayName: "New", DefaultProject: "new", Instance: "inst-b", Dir: "/elsewhere",
		Limits:  Limits{MaxSectionsPerPad: 5000},
		Wait:    Wait{DefaultS: 30, MaxS: 300},
		Rules:   RulesPolicy{Store: RulesWriteAgent, Project: RulesWriteAgent, Pad: RulesWriteAny},
		TCP:     TCP{Port: 9999, TokenDigests: []string{"sha256:bbb"}},
		UI:      UI{Port: 8888},
		RootDir: "/fresh/root", ProjectsDir: "/fresh/root/projects",
		SocketPath: "/fresh/root/inst-b.sock",
	}

	got := MergeHot(running, fresh)

	if got.DisplayName != "New" || got.DefaultProject != "new" {
		t.Errorf("identity strings not adopted: %+v", got)
	}
	if got.Limits != fresh.Limits || got.Wait != fresh.Wait || got.Rules != fresh.Rules {
		t.Errorf("hot groups not adopted: %+v", got)
	}
	if got.Instance != "inst-a" || got.Dir != "/somewhere" || got.UI != running.UI || got.TCP.Port != 1111 {
		t.Errorf("cold groups leaked through: %+v", got)
	}
	if got.RootDir != running.RootDir || got.ProjectsDir != running.ProjectsDir || got.SocketPath != running.SocketPath {
		t.Errorf("derived paths were replaced: %+v", got)
	}
}

func TestColdChanges(t *testing.T) {
	base := Config{
		Instance: "a", Dir: "/d",
		TCP: TCP{Port: 1, TokenDigests: []string{"x"}, AllowedOrigins: []string{"o"}},
		UI:  UI{Port: 2},
	}
	for _, tc := range []struct {
		name string
		edit func(*Config)
		want []string
	}{
		{"nothing", func(*Config) {}, nil},
		{"hot only", func(c *Config) { c.Limits.MaxSectionsPerPad = 99; c.DisplayName = "x" }, nil},
		{"instance", func(c *Config) { c.Instance = "b" }, []string{"instance"}},
		{"dir", func(c *Config) { c.Dir = "/e" }, []string{"dir"}},
		{"tcp port", func(c *Config) { c.TCP.Port = 7 }, []string{"tcp"}},
		{"tcp tokens", func(c *Config) { c.TCP.TokenDigests = []string{"y"} }, []string{"tcp"}},
		{"tcp origins", func(c *Config) { c.TCP.AllowedOrigins = nil }, []string{"tcp"}},
		{"ui no_auth", func(c *Config) { c.UI.NoAuth = true }, []string{"ui"}},
		{"several", func(c *Config) { c.Instance = "b"; c.UI.Port = 3 }, []string{"instance", "ui"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := base
			// Copy the slices so a case that replaces one does not alias the base.
			next.TCP.TokenDigests = slices.Clone(base.TCP.TokenDigests)
			next.TCP.AllowedOrigins = slices.Clone(base.TCP.AllowedOrigins)
			tc.edit(&next)
			if got := ColdChanges(base, next); !slices.Equal(got, tc.want) {
				t.Fatalf("ColdChanges = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestLiveConcurrentReadWrite is the case the whole holder exists for: a reload landing
// while requests are reading. Run under -race, this is what proves the swap is safe.
func TestLiveConcurrentReadWrite(t *testing.T) {
	live := NewLive(Config{Limits: DefaultLimits})

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				if live.Get().Limits.MaxSectionsPerPad <= 0 {
					t.Error("read a zero limit; a snapshot should never be half-installed")
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 200 {
			cfg := DefaultLimits
			cfg.MaxSectionsPerPad = 1 + i
			live.Set(Config{Limits: cfg})
		}
	}()
	wg.Wait()
}

func TestLiveApplyReportsColdAndAppliesHot(t *testing.T) {
	live := NewLive(Config{
		Instance: "running", UI: UI{Port: 6711},
		Limits: Limits{MaxSectionsPerPad: 10}, RootDir: "/run",
	})

	cold := live.Apply(Config{
		Instance: "edited", UI: UI{Port: 9999},
		Limits: Limits{MaxSectionsPerPad: 5000}, RootDir: "/elsewhere",
	})

	if !slices.Equal(cold, []string{"instance", "ui"}) {
		t.Fatalf("cold changes = %v", cold)
	}
	got := live.Get()
	if got.Limits.MaxSectionsPerPad != 5000 {
		t.Errorf("hot group not applied: %+v", got.Limits)
	}
	if got.Instance != "running" || got.UI.Port != 6711 || got.RootDir != "/run" {
		t.Errorf("cold group applied anyway: %+v", got)
	}
}
