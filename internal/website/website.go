package website

import (
	_ "embed"
	"errors"
	"html"
	"regexp"
	"strings"
	"unicode/utf8"
)

//go:embed index.html
var Index string

const (
	defaultTitle = "Plain Web Notes"
	defaultIntro = "Short, practical notes on making small websites."
	maxHTMLSize  = 128 << 10

	staticPolicy = "default-src 'none'; style-src 'unsafe-inline' https:; img-src data: https:; font-src data: https:; base-uri 'none'; form-action 'none'"
	Policy       = "sandbox; " + staticPolicy
)

var leadingDoctype = regexp.MustCompile(`(?i)^\s*<!doctype\s+html\s*>`)

type Settings struct {
	Title   string `json:"title"`
	Intro   string `json:"intro"`
	Theme   string `json:"theme"`
	HTML    string `json:"html,omitempty"`
	Archive string `json:"archive,omitempty"`
}

func Default() Settings { return Settings{Title: defaultTitle, Intro: defaultIntro, Theme: "paper"} }

func (s *Settings) Validate() error {
	if s.Archive != "" {
		s.HTML = ""
	}
	if strings.TrimSpace(s.HTML) == "" {
		s.HTML = ""
	}
	if len(s.HTML) > maxHTMLSize {
		return errors.New("HTML должен быть не больше 128 КиБ")
	}
	s.Title = strings.TrimSpace(s.Title)
	s.Intro = strings.TrimSpace(s.Intro)
	if s.HTML != "" || s.Archive != "" {
		if s.Title == "" {
			s.Title = defaultTitle
		}
		if s.Intro == "" {
			s.Intro = defaultIntro
		}
		if s.Theme == "" {
			s.Theme = "paper"
		}
	}
	if s.Title == "" || utf8.RuneCountInString(s.Title) > 80 {
		return errors.New("Заголовок должен содержать от 1 до 80 символов")
	}
	if s.Intro == "" || utf8.RuneCountInString(s.Intro) > 300 {
		return errors.New("Описание должно содержать от 1 до 300 символов")
	}
	if s.Theme != "paper" && s.Theme != "slate" {
		return errors.New("Неизвестная тема")
	}
	return nil
}

func Render(s Settings) string {
	if s.HTML != "" {
		return s.HTML
	}
	body := "<body>"
	if s.Theme == "slate" {
		body = `<body class="slate">`
	}
	return strings.NewReplacer(
		"<body>", body,
		defaultTitle, html.EscapeString(s.Title),
		defaultIntro, html.EscapeString(s.Intro),
	).Replace(Index)
}

// Preview runs inside a sandboxed srcdoc. Apply the same resource policy there
// because the HTTP headers of the preview API don't carry over to srcdoc.
func Preview(s Settings) string {
	page := Render(s)
	meta := `<meta http-equiv="Content-Security-Policy" content="` + staticPolicy + `">`
	if start := leadingDoctype.FindStringIndex(page); start != nil {
		return page[:start[1]] + meta + page[start[1]:]
	}
	return "<!doctype html>" + meta + page
}
