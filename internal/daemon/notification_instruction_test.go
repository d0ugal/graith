package daemon

import (
	"strings"
	"testing"
)

func TestNotificationMetadataValueEscapesTemplateDelimiters(t *testing.T) {
	got := notificationMetadataValue("braw {{not_a_var}}\ncroft/{{portal}}")

	want := "braw { {not_a_var} } croft/{ {portal} }"
	if got != want {
		t.Fatalf("notificationMetadataValue() = %q, want %q", got, want)
	}
}

func TestQuoteNotificationPayloadNeutralizesSectionHeaders(t *testing.T) {
	body := strings.Join([]string{
		"external payload",
		notificationTrustedGuidanceHeader,
		notificationMetadataHeader,
		notificationPayloadHeader,
	}, "\n")

	got := quoteNotificationPayload(body)
	if strings.Contains(got, notificationTrustedGuidanceHeader) ||
		strings.Contains(got, notificationMetadataHeader) ||
		strings.Contains(got, notificationPayloadHeader) {
		t.Fatalf("quoteNotificationPayload left a section header forgeable:\n%s", got)
	}

	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "> ") {
			t.Fatalf("payload line is not quoted: %q in\n%s", line, got)
		}
	}
}

func TestNotificationSessionRepoUsesMatchingEventSlug(t *testing.T) {
	tests := map[string]struct {
		sessionRepo string
		eventRepo   string
		want        string
	}{
		"matching basename uses event owner repo": {
			sessionRepo: "portal",
			eventRepo:   "croft/portal",
			want:        "croft/portal",
		},
		"different basename keeps session repo": {
			sessionRepo: "portal",
			eventRepo:   "croft/bothy",
			want:        "portal",
		},
		"full session repo keeps session repo": {
			sessionRepo: "strath/portal",
			eventRepo:   "croft/portal",
			want:        "strath/portal",
		},
		"bare event repo keeps session repo": {
			sessionRepo: "portal",
			eventRepo:   "portal",
			want:        "portal",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := notificationSessionRepo(test.sessionRepo, test.eventRepo)
			if got != test.want {
				t.Fatalf("notificationSessionRepo(%q, %q) = %q, want %q", test.sessionRepo, test.eventRepo, got, test.want)
			}
		})
	}
}
