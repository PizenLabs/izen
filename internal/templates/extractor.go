package templates

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	reSingleQuote = regexp.MustCompile(`'([^']*)'`)
	reDoubleQuote = regexp.MustCompile(`"([^"]*)"`)
	reAuthorKW    = regexp.MustCompile(`(?i)\bauthors?\s+(\S+)`)
	reByKW        = regexp.MustCompile(`(?i)\bby\s+(\S+)`)
	reYear        = regexp.MustCompile(`\b(19[0-9][0-9]|20[0-9][0-9])\b`)
)

type LicenseParams struct {
	Year   string
	Author string
}

func ExtractLicenseParams(prompt string) LicenseParams {
	p := LicenseParams{
		Year:   strconv.Itoa(time.Now().Year()),
		Author: gitConfigUser(),
	}

	if m := reSingleQuote.FindStringSubmatch(prompt); m != nil {
		p.Author = m[1]
	} else if m := reDoubleQuote.FindStringSubmatch(prompt); m != nil {
		p.Author = m[1]
	} else if m := reAuthorKW.FindStringSubmatch(prompt); m != nil {
		candidate := m[1]
		if !isYear(candidate) {
			p.Author = candidate
		}
	} else if m := reByKW.FindStringSubmatch(prompt); m != nil {
		candidate := m[1]
		if !isYear(candidate) {
			p.Author = candidate
		}
	}

	if m := reYear.FindString(prompt); m != "" {
		p.Year = m
	}

	return p
}

func isYear(s string) bool {
	if len(s) != 4 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func gitConfigUser() string {
	ctx := context.Background()
	out, err := exec.CommandContext(ctx, "git", "config", "user.name").Output()
	if err != nil {
		return "Author Name"
	}
	name := strings.TrimSpace(string(out))
	if name == "" {
		return "Author Name"
	}
	return name
}
