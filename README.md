# glade

A small status page. glade checks your websites and ports every minute
or so, keeps up to 90 days of results, and serves one page: how each one
is doing now, a row of bars for its history, and incidents you write in
markdown.

It is one Go binary with its page and CSS built in. The page needs no
JavaScript: it reloads itself after every round of checks, so a page
left open stays current.

    go build
    ./glade -config example/glade.toml     # http://127.0.0.1:8080/

To see a page with 90 days behind it, outages and incidents included,
try the [demo](#demo).

## The page

- **At the top**, how everything is: "All systems normal", "example.net
  is having problems", and when the last check ran.
- **Ongoing incidents**, if any, under it.
- **A row for each check**: up, degraded or down and for how long, why
  if it isn't up ("503 Service Unavailable", "connection refused"), its
  uptime, and a row of bars for its history. Hold the pointer over a bar
  for that period's uptime. On a phone, the row is the last 30 bars.
- **Past incidents**, resolved in the last 90 days.

The bars grow with glade's history, so a new page isn't mostly grey:

| glade has been running | The bars show | One bar is |
|---|---|---|
| up to an hour | the last hour | a minute |
| up to a day | the last day | half an hour |
| up to a week | the last week | two hours |
| up to 4 weeks | the last 4 weeks | a day |
| longer | 40, 50, 60, 70, 80, then 90 days | a day |

A bar is green when every check in its time passed, orange when some
failed or were degraded, red when more than a tenth failed, and grey
when nothing was checked. The uptime is over the time the bars show, and
counts degraded as up.

It also serves `/feed.xml`, the incidents as an Atom feed, and
`/status.json`, each check's state for scripts and other monitors, and
when the last round of checks ended (`checked`, left out until the first
one has).

## Checks

    title = "spinney status"
    listen = "127.0.0.1:8080"
    interval = "1m"

    [[check]]
    name = "Website"
    url = "https://example.com/"
    contains = "Example Domain"
    slow = "2s"

    [[check]]
    name = "SSH"
    tcp = "git.example.com:22"

A check is a `url` or a `tcp` address:

- **url**: an HTTP or HTTPS page, which must answer with a 2xx status
  after following redirects, and contain the `contains` text if that is
  set. On HTTPS, a certificate that expires within 14 days makes it
  degraded.
- **tcp**: a `host:port` that must accept a connection.

Each also takes a `timeout` (10s by default) and a `slow` time: an
answer slower than that is degraded. A check that fails is tried once
more two seconds later before it counts as down, so that one lost packet
isn't an outage.

The page gives the reason in a few plain words: "no answer within 10s",
"the certificate has expired". It never shows the error itself, which
can include the check's URL, with any token in it, and internal names
and addresses; that goes to glade's log, when the check changes state.

Every setting, with its default:

| Setting | Default | |
|---|---|---|
| `title` | `"Status"` | the page's title |
| `url` | `http://` + `listen` | the page's public address, for the links in the feed |
| `listen` | `"127.0.0.1:8080"` | where to serve; `-listen` overrides it |
| `interval` | `"1m"` | between rounds of checks, at least `10s` |
| `database` | `"glade.db"` | the results, in SQLite, beside the config |
| `incidents` | `"incidents"` | the incidents folder, beside the config |
| `on_change` | | a command to run when a check changes state |

All the checks run at once, every `interval`, and every result goes
into `database`, an SQLite file: when it was, up or down, how long it
took and why it failed. Results older than 90 days are deleted. For
three checks a minute, 90 days is about 400,000 results and 20 MB, and
glade reads them back in about a second when it starts, so it picks up
where it left off. The results are kept by check name: renaming a check
starts its history again.

To look at them yourself, with the `sqlite3` program:

    sqlite3 glade.db "SELECT check_name, datetime(time / 1000, 'unixepoch'), note
                      FROM results WHERE state = 'down' ORDER BY time DESC LIMIT 20"

## Incidents

An incident is a markdown file in `incidents/`, with a title and when it
started, in UTC, between two `---` lines:

    ---
    title: example.net unreachable
    date: 2026-10-15 14:20
    ---

    **14:20** The server isn't answering. We're looking into it.

While it has no `resolved:`, it shows at the top of the page as ongoing.
Add one when it's over, and it moves to past incidents:

    resolved: 2026-10-15 15:05

Dates are `2026-10-15 14:20` or just `2026-10-15`. glade reads the folder
again every ten seconds while the page is being loaded, so a new or
changed incident shows within ten seconds, without a restart.

The file's name, without `.md`, is the incident's anchor on the page
(`/#2026-10-15-git`) and its id in the feed. If a file can't be read,
glade won't start; on a running server, the page says so, the log says
why, and the incidents read before stay on the page.

## Alerts

glade sends nothing itself. `on_change` is a command run with `sh -c`
whenever a check goes from one state to another, with the change in its
environment:

| Variable | |
|---|---|
| `GLADE_CHECK` | the check's name |
| `GLADE_STATE` | `up`, `degraded` or `down` |
| `GLADE_PREVIOUS` | the state before |
| `GLADE_NOTE` | why it isn't up, such as `503 Service Unavailable` |

For a notification on your phone with [ntfy](https://ntfy.sh):

    on_change = 'curl -s -d "$GLADE_CHECK is $GLADE_STATE $GLADE_NOTE" https://ntfy.sh/your-topic'

## Running it on a server

Run glade somewhere other than what it watches: if it shares a server
with your site, it goes down with it, just when it is needed. A small
VPS of its own is enough.

Serve it behind a web server for HTTPS, such as Caddy:

    status.example.com {
        reverse_proxy 127.0.0.1:8080
    }

and set `url = "https://status.example.com/"` in the config, for the
feed.

and keep it running with systemd, in `/etc/systemd/system/glade.service`:

    [Unit]
    Description=glade status page
    After=network-online.target
    Wants=network-online.target

    [Service]
    ExecStart=/usr/local/bin/glade -config /etc/glade/glade.toml
    DynamicUser=yes
    StateDirectory=glade
    Restart=always

    [Install]
    WantedBy=multi-user.target

with `database = "/var/lib/glade/glade.db"` in the config: systemd
runs glade as a user of its own, which can write only there.

## Demo

`go run ./demo` makes up 90 days of history for the checks in
`demo/glade.toml`: a result a minute, ending now, with a few outages, a
slow afternoon and a certificate about to expire, and incidents in
`demo/incidents` to go with them. Then run glade on it:

    go run ./demo
    go run . -config demo/glade.toml        # http://127.0.0.1:8080/

The checks carry on live from there, every 10 seconds so the page stays
current, against the example sites, and show each state: two sites up,
example.net degraded (its `slow` is less than any HTTPS round trip) and
a mail server down (nothing listens on its port), with an ongoing
incident for it.

`-age` makes less history, to see the bars at each size:

    go run ./demo -age 30m      # the last hour, in minutes
    go run ./demo -age 5h       # the last day, in half-hours
    go run ./demo -age 3d       # the last week, in two hours
    go run ./demo -age 45d      # 50 days

Each run replaces `demo/glade.db` and `demo/incidents`.

## Development

    go test ./...
    go vet ./...
    gofmt -l .

## License

[GNU GPL v3](LICENSE), or any later version.

    glade, a small status page.
    Copyright (C) 2026 The spinney authors

    This program is free software: you can redistribute it and/or modify it
    under the terms of the GNU General Public License as published by the
    Free Software Foundation, either version 3 of the License, or (at your
    option) any later version.

    This program is distributed in the hope that it will be useful, but
    WITHOUT ANY WARRANTY; without even the implied warranty of
    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU General
    Public License for more details.
