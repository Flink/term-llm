package config

import "github.com/term-llm/term-llm/internal/proxyprofile"

func defaultProxySlot(slot string) bool {
	switch slot {
	case "search", "fetch", "image", "audio", "music", "transcription":
		return true
	}
	return false
}

func (c *Config) resolveProxySlots(file *fileSettings, defaults map[string]proxyDiscoveredDefault, served map[proxyDiscoveredDefault]proxyprofile.Profile) {
	for _, slot := range []struct {
		name, key, builtin string
		selector           *string
	}{
		{"search", "search.provider", DefaultSearchProvider, &c.Search.Provider},
		{"fetch", "search.fetch_provider", DefaultSearchFetchProvider, &c.Search.FetchProvider},
		{"image", "image.provider", DefaultImageProvider, &c.Image.Provider},
		{"audio", "audio.provider", DefaultAudioProvider, &c.Audio.Provider},
		{"music", "music.provider", DefaultMusicProvider, &c.Music.Provider},
		{"transcription", "transcription.provider", DefaultTranscriptionProvider, &c.Transcription.Provider},
	} {
		if file != nil && file.has(slot.key) || file == nil && *slot.selector != "" && *slot.selector != slot.builtin {
			continue
		}
		if d := served[defaults[slot.name]].Slots[slot.name]; d != "" {
			*slot.selector = d
			continue
		}
		*slot.selector = slot.builtin
		if slot.name != "search" && slot.name != "fetch" {
			continue
		}
		choices := map[string]bool{}
		for _, p := range served {
			if v := p.Slots[slot.name]; v != "" {
				choices[v] = true
			}
		}
		if len(choices) == 1 {
			for v := range choices {
				*slot.selector = v
			}
		}
	}
}
