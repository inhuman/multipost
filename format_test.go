package main

import "testing"

func TestToHTML(t *testing.T) {
	cases := map[string]string{
		"**жирный** и *курсив*":                   "<b>жирный</b> и <i>курсив</i>",
		"a < b & c":                               "a &lt; b &amp; c",
		"[канал](https://t.me/sim_to_real_robot)": `<a href="https://t.me/sim_to_real_robot">канал</a>`,
		"`x**2*y`":                    "<code>x**2*y</code>",
		"[a*b](https://ex.com/a*b*c)": `<a href="https://ex.com/a*b*c">a*b</a>`,
		"код `<b>` и [x](https://e.com/?a=1&b=2)": `код <code>&lt;b&gt;</code> и <a href="https://e.com/?a=1&amp;b=2">x</a>`,
	}
	for in, want := range cases {
		if got := toHTML(in); got != want {
			t.Errorf("toHTML(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToPlain(t *testing.T) {
	cases := map[string]string{
		"**жирный** `код`":                 "жирный код",
		"[канал](https://t.me/x)":          "канал (https://t.me/x)",
		"[https://t.me/x](https://t.me/x)": "https://t.me/x",
	}
	for in, want := range cases {
		if got := toPlain(in); got != want {
			t.Errorf("toPlain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTextLenSkipsMarkupAndURL(t *testing.T) {
	if n := textLen("**аб** [в](https://example.com/long)"); n != 4 {
		t.Errorf("textLen = %d, want 4", n)
	}
}
