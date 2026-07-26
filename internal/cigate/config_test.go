package cigate

import (
	"strings"
	"testing"
)

func TestConfigFailsClosedWithoutExternalRuntimeAndKeyDecisions(t *testing.T) {
	tests := map[string]struct {
		mutate func(*Config)
		want   string
	}{
		"runtime missing": {
			mutate: func(config *Config) { config.Deployment.Runtime = "" },
			want:   "deployment runtime",
		},
		"runtime pending": {
			mutate: func(config *Config) { config.Deployment.Runtime = "pending" },
			want:   "deployment runtime",
		},
		"runtime localhost url": {
			mutate: func(config *Config) { config.Deployment.Runtime = "http://localhost:8080" },
			want:   "deployment runtime",
		},
		"runtime pending suffix": {
			mutate: func(config *Config) { config.Deployment.Runtime = "pending-runtime" },
			want:   "deployment runtime",
		},
		"attestation service missing": {
			mutate: func(config *Config) { config.Deployment.AttestationKey.Service = "" },
			want:   "attestation key service",
		},
		"attestation service local prefix": {
			mutate: func(config *Config) { config.Deployment.AttestationKey.Service = "local-kms" },
			want:   "attestation key service",
		},
		"app owner placeholder": {
			mutate: func(config *Config) { config.App.Owner = "tbd" },
			want:   "app owner",
		},
		"installation owner placeholder": {
			mutate: func(config *Config) { config.App.InstallationOwner = "pending-owner" },
			want:   "installation owner",
		},
		"key id placeholder": {
			mutate: func(config *Config) { config.Deployment.AttestationKey.KeyID = "pending-key" },
			want:   "key id",
		},
		"trust model missing": {
			mutate: func(config *Config) { config.Deployment.AttestationKey.TrustModel = "" },
			want:   "trust model",
		},
		"rotation owner missing": {
			mutate: func(config *Config) { config.Deployment.Rotation.Owner = "" },
			want:   "rotation owner",
		},
		"rotation owner placeholder": {
			mutate: func(config *Config) { config.Deployment.Rotation.Owner = "tbd" },
			want:   "rotation owner",
		},
		"rotation cadence placeholder": {
			mutate: func(config *Config) { config.Deployment.Rotation.Cadence = "todo" },
			want:   "rotation cadence",
		},
		"rotation runbook placeholder": {
			mutate: func(config *Config) { config.Deployment.Rotation.Runbook = "pending" },
			want:   "rotation runbook",
		},
		"revocation runbook placeholder": {
			mutate: func(config *Config) { config.Deployment.IncidentRevocation.Runbook = "unset" },
			want:   "incident revocation runbook",
		},
		"retention owner placeholder": {
			mutate: func(config *Config) { config.Retention.Owner = "local-operator" },
			want:   "retention owner",
		},
		"retention location placeholder": {
			mutate: func(config *Config) { config.Retention.Location = "local-store" },
			want:   "retention location",
		},
		"operator owner placeholder": {
			mutate: func(config *Config) { config.Operators = []string{"pending-operator"} },
			want:   "operator owner",
		},
		"retention too short": {
			mutate: func(config *Config) { config.Retention.Duration = "24h" },
			want:   "90-day evidence floor",
		},
		"fixture repository missing": {
			mutate: func(config *Config) { config.LiveProof.FixtureRepository = "" },
			want:   "fixture repository",
		},
		"fixture repository placeholder": {
			mutate: func(config *Config) { config.LiveProof.FixtureRepository = "todo-fixture" },
			want:   "fixture repository",
		},
		"fixture repository is protected repository": {
			mutate: func(config *Config) { config.LiveProof.FixtureRepository = config.Repository },
			want:   "fixture repository must be separate",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			config := validConfig()
			test.mutate(&config)

			err := config.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestAppContractEnforcesLeastPrivilege(t *testing.T) {
	t.Run("allows optional statuses write", func(t *testing.T) {
		config := validConfig()
		config.App.Permissions["statuses"] = "write"

		if err := config.Validate(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("rejects extra permission", func(t *testing.T) {
		config := validConfig()
		config.App.Permissions["contents"] = "write"

		err := config.Validate()
		if err == nil || !strings.Contains(err.Error(), "contents") {
			t.Fatalf("Validate() error = %v, want contents permission rejection", err)
		}
	})

	t.Run("rejects missing merge_group event", func(t *testing.T) {
		config := validConfig()
		config.App.Events = []string{"pull_request"}

		err := config.Validate()
		if err == nil || !strings.Contains(err.Error(), "merge_group") {
			t.Fatalf("Validate() error = %v, want merge_group event rejection", err)
		}
	})
}
