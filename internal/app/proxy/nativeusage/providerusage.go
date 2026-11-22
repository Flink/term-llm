package nativeusage

import (
	"errors"
	"fmt"
	"strings"

	"github.com/term-llm/term-llm/internal/llm/providerusage"
)

// DecodeChatGPT preserves the Codex quota and all additional metered features.
func DecodeChatGPT(body []byte) (Report, error) {
	return decodeProviderUsage(body, providerusage.DecodeChatGPT)
}

// DecodeGrok normalizes the same CLI billing response used by direct usage.
func DecodeGrok(body []byte) (Report, error) {
	return decodeProviderUsage(body, providerusage.DecodeGrok)
}

// DecodeOpenCodeGo preserves rolling, weekly and monthly subscription quotas.
func DecodeOpenCodeGo(body []byte) (Report, error) {
	return decodeProviderUsage(body, providerusage.DecodeOpenCodeGo)
}

func decodeProviderUsage(body []byte, decode func([]byte) (*providerusage.Report, error)) (Report, error) {
	raw, err := decode(body)
	if err != nil {
		return Report{}, err
	}
	out := Report{Plan: raw.Plan, Detail: strings.Join(strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(raw.LimitState)), " ")}
	if credits := raw.Credits; credits != nil {
		var detail string
		switch {
		case credits.Unlimited:
			detail = "Unlimited credits"
		case credits.HasCredits && strings.TrimSpace(credits.Balance) != "":
			detail = strings.TrimSpace(credits.Balance+" "+credits.Currency) + " credits remaining"
		}
		if detail != "" {
			if out.Detail != "" {
				out.Detail += " · "
			}
			out.Detail += detail
		}
	}
	for _, limit := range raw.Limits {
		group := Group{Name: strings.TrimSpace(limit.Name)}
		if group.Name == "" {
			group.Name = limit.ID
		}
		for _, w := range []*providerusage.Window{limit.PrimaryWindow, limit.SecondaryWindow} {
			if w == nil {
				continue
			}
			window := Window{Label: w.Label, Detail: w.Detail, DurationMinutes: w.DurationMinutes, UsedPercent: w.UsedPercent, PercentUnknown: w.PercentUnknown}
			if window.Label == "" {
				window.Label = windowLabel(window.DurationMinutes)
			}
			if !w.ResetsAt.IsZero() {
				window.ResetsAt = &w.ResetsAt
			}
			group.Windows = append(group.Windows, window)
		}
		if limit.LimitReached {
			if len(group.Windows) == 1 {
				group.Windows[0].LimitReached = true
			} else {
				// The upstream flag belongs to the limit, not each contributing window.
				group.Windows = append(group.Windows, Window{Label: "Limit status", PercentUnknown: true, LimitReached: true})
			}
		}
		if len(group.Windows) != 0 {
			out.Groups = append(out.Groups, group)
		}
	}
	if len(out.Groups) == 0 {
		return Report{}, errors.New("upstream did not report quota windows")
	}
	return out, nil
}

func windowLabel(minutes int) string {
	switch {
	case minutes <= 0:
		return "Usage window"
	case minutes == 7*24*60:
		return "Weekly"
	case minutes%1440 == 0:
		return fmt.Sprintf("%d days", minutes/1440)
	case minutes%60 == 0:
		return fmt.Sprintf("%d hours", minutes/60)
	default:
		return fmt.Sprintf("%d min", minutes)
	}
}
