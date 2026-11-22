package website

import (
	_ "embed"
	"errors"
	"html"
	"strings"
	"unicode/utf8"
)

//go:embed index.html
var Index string

const (
	defaultTitle = "Plain Web Notes"
	defaultIntro = "Short, practical notes on making small websites."
)

type Settings struct {
	Title string `json:"title"`
	Intro string `json:"intro"`
	Theme string `json:"theme"`
}

func Default() Settings { return Settings{Title: defaultTitle, Intro: defaultIntro, Theme: "paper"} }

func (s *Settings) Validate() error {
	s.Title = strings.TrimSpace(s.Title)
	s.Intro = strings.TrimSpace(s.Intro)
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
