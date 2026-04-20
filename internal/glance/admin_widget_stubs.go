package glance

// knownWidgetTypes returns every widget type that newWidget() accepts. Keep
// in lockstep with the switch in widget.go; the stub test catches drift.
func knownWidgetTypes() []string {
	return []string{
		"calendar", "calendar-legacy", "clock", "weather", "bookmarks",
		"iframe", "html", "hacker-news", "releases", "videos",
		"markets", "reddit", "rss", "monitor", "twitch-top-games",
		"twitch-channels", "lobsters", "change-detection", "repository",
		"search", "extension", "group", "dns-stats", "split-column",
		"custom-api", "docker-containers", "server-stats", "to-do",
	}
}

// widgetStubs maps widget type to a ready-to-paste YAML block beginning with
// "- type:" so callers append directly after a "widgets:" key.
var widgetStubs = map[string]string{
	"calendar":          "- type: calendar\n  first-day-of-week: monday\n",
	"calendar-legacy":   "- type: calendar-legacy\n",
	"clock":             "- type: clock\n  hour-format: 24h\n",
	"weather":           "- type: weather\n  location: London, United Kingdom\n  units: metric\n",
	"bookmarks":         "- type: bookmarks\n  groups:\n    - links:\n        - title: Example\n          url: https://example.com\n",
	"iframe":            "- type: iframe\n  source: https://example.com\n",
	"html":              "- type: html\n  source: |\n    <p>Hello</p>\n",
	"hacker-news":       "- type: hacker-news\n  limit: 10\n",
	"releases":          "- type: releases\n  repositories:\n    - glanceapp/glance\n",
	"videos":            "- type: videos\n  channels:\n    - UCXuqSBlHAE6Xw-yeJA0Tunw\n",
	"markets":           "- type: markets\n  markets:\n    - symbol: SPY\n      name: S&P 500\n",
	"reddit":            "- type: reddit\n  subreddit: selfhosted\n",
	"rss":               "- type: rss\n  feeds:\n    - url: https://example.com/feed.xml\n",
	"monitor":           "- type: monitor\n  sites:\n    - title: Example\n      url: https://example.com\n",
	"twitch-top-games":  "- type: twitch-top-games\n  limit: 10\n",
	"twitch-channels":   "- type: twitch-channels\n  channels:\n    - theprimeagen\n",
	"lobsters":          "- type: lobsters\n  limit: 10\n",
	"change-detection":  "- type: change-detection\n  instance-url: http://localhost:5000\n  token: REDACTED\n",
	"repository":        "- type: repository\n  repository: glanceapp/glance\n",
	"search":            "- type: search\n  search-engine: duckduckgo\n",
	"extension":         "- type: extension\n  url: https://example.com/my-extension\n",
	"group":             "- type: group\n  widgets:\n    - type: hacker-news\n    - type: lobsters\n",
	"dns-stats":         "- type: dns-stats\n  service: pihole\n  url: http://pihole.local\n  password: REDACTED\n",
	"split-column":      "- type: split-column\n  widgets:\n    - type: hacker-news\n    - type: lobsters\n",
	"custom-api":        "- type: custom-api\n  url: https://api.example.com/data\n  template: |\n    <div>{{.JSON.String \"title\"}}</div>\n",
	"docker-containers": "- type: docker-containers\n  hide-by-default: false\n",
	"server-stats":      "- type: server-stats\n",
	"to-do":             "- type: to-do\n",
}
