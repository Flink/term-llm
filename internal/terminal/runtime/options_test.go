package tea

import (
	"bytes"
	"context"
	"os"
	"testing"
)

func TestOptions(t *testing.T) {
	t.Run("output", func(t *testing.T) {
		var b bytes.Buffer
		p := NewProgram(nil, WithOutput(&b))
		if f, ok := p.output.(*os.File); ok {
			t.Errorf("expected output to custom, got %v", f.Fd())
		}
	})

	t.Run("external context", func(t *testing.T) {
		extCtx, extCancel := context.WithCancel(context.Background())
		defer extCancel()

		p := NewProgram(nil, WithContext(extCtx))
		if p.externalCtx != extCtx || p.externalCtx == context.Background() {
			t.Errorf("expected passed in external context, got default")
		}
	})

	t.Run("input options", func(t *testing.T) {
		exercise := func(t *testing.T, opt ProgramOption, fn func(*Program)) {
			p := NewProgram(nil, opt)
			fn(p)
		}

		t.Run("nil input", func(t *testing.T) {
			exercise(t, WithInput(nil), func(p *Program) {
				if !p.disableInput || p.input != nil {
					t.Errorf("expected input to be disabled, got %v", p.input)
				}
			})
		})

		t.Run("custom input", func(t *testing.T) {
			var b bytes.Buffer
			exercise(t, WithInput(&b), func(p *Program) {
				if p.input != &b {
					t.Errorf("expected input to be custom, got %v", p.input)
				}
			})
		})
	})

	t.Run("startup options", func(t *testing.T) {
		exercise := func(t *testing.T, opt ProgramOption, fn func(*Program)) {
			p := NewProgram(nil, opt)
			fn(p)
		}

		t.Run("without signal handler", func(t *testing.T) {
			exercise(t, WithoutSignalHandler(), func(p *Program) {
				if !p.disableSignalHandler {
					t.Errorf("expected signal handler to be disabled")
				}
			})
		})
	})
}

func TestWithEmojiWidth(t *testing.T) {
	for _, configured := range []bool{true, false} {
		var output bytes.Buffer
		opts := []ProgramOption{WithInput(nil), WithOutput(&output), WithoutSignalHandler()}
		if configured {
			opts = append(opts, WithEmojiWidth(func(s string) int {
				if s == "☀️" {
					return 2
				}
				return 0
			}))
		}
		p := NewProgram(kittyNoInputModel{}, opts...)
		if _, err := p.Run(); err != nil {
			t.Fatal(err)
		}
		got := p.renderer.(*cursedRenderer).cellbuf.EmojiWidth
		if !configured {
			if got != nil {
				t.Fatal("default renderer unexpectedly has a wide emoji width function")
			}
		} else if got == nil || got("☀️") != 2 || got("A") != 0 {
			t.Fatal("screen buffer did not receive the configured wide emoji width function")
		}
	}
}
