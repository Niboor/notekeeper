#!/usr/bin/env python3
"""Writes the Grafana dashboards (deploy/grafana/*.json) from the panel definitions below.

Grafana's JSON is long and repetitive, so the dashboards are described here and generated:
`make generate` runs this script, and `make generate-check` fails when the committed JSON differs.
Edit this file, not the JSON.

Every query refers to the datasource through the `datasource` variable and to the scrape jobs of Core and the bot
through the `core` and `bot` variables (job names differ between setups), so the files import as they are.
"""
import json
import pathlib

OUT = pathlib.Path(__file__).parent
DS = {"type": "prometheus", "uid": "${datasource}"}
CORE = 'job=~"$core"'
BOT = 'job=~"$bot"'

# ---- colours: the same meaning everywhere ---------------------------------------------------------------------------
GOOD, WARN, BAD = "green", "orange", "red"


class Board:
    """One dashboard: rows of panels laid out on Grafana's 24-column grid."""

    def __init__(self, uid, title, description, variables):
        self.uid, self.title, self.description, self.variables = uid, title, description, variables
        self.panels = []
        self.y = 0
        self._id = 0

    def row(self, title):
        self._id += 1
        self.panels.append({"type": "row", "id": self._id, "title": title, "collapsed": False, "gridPos": {"h": 1, "w": 24, "x": 0, "y": self.y}, "panels": []})
        self.y += 1

    def line(self, *panels, h=8):
        """Puts panels side by side, sharing the 24 columns equally (or by their own `w`)."""
        default = 24 // len(panels)
        x = 0
        for p in panels:
            w = p.pop("w", default)
            self._id += 1
            p["id"] = self._id
            p["gridPos"] = {"h": h, "w": w, "x": x, "y": self.y}
            self.panels.append(p)
            x += w
        self.y += h

    def json(self):
        return {
            "uid": self.uid,
            "title": self.title,
            "description": self.description,
            "tags": ["notekeeper"],
            "timezone": "browser",
            "editable": True,
            "graphTooltip": 1,
            "schemaVersion": 39,
            "version": 1,
            "refresh": "30s",
            "time": {"from": "now-6h", "to": "now"},
            "timepicker": {},
            "annotations": {"list": []},
            "links": [{"type": "dashboards", "title": "Notekeeper", "tags": ["notekeeper"], "asDropdown": True, "includeVars": False, "keepTime": True, "icon": "external link"}],
            "templating": {"list": self.variables},
            "panels": self.panels,
        }


def variables(core=False, bot=False):
    v = [{"name": "datasource", "label": "Data source", "type": "datasource", "query": "prometheus", "current": {}, "hide": 0, "refresh": 1}]
    if core:
        v.append(query_var("core", "Core", "label_values(nk_http_requests_total, job)"))
    if bot:
        v.append(query_var("bot", "Bot", "label_values(nk_bot_last_sync_timestamp_seconds, job)"))
    return v


def query_var(name, label, query):
    return {"name": name, "label": label, "type": "query", "datasource": DS, "query": {"query": query, "refId": "v"}, "definition": query,
            "current": {}, "includeAll": True, "allValue": ".+", "multi": True, "refresh": 2, "sort": 1, "hide": 0}


def thresholds(steps):
    """steps: [(value or None for the base, colour)]"""
    return {"mode": "absolute", "steps": [{"value": v, "color": c} for v, c in steps]}


def targets(exprs):
    """exprs: an expression, or a list of (expression, legend)."""
    if isinstance(exprs, str):
        exprs = [(exprs, "")]
    return [{"refId": chr(65 + i), "datasource": DS, "expr": e, "legendFormat": leg, "range": True, "instant": False} for i, (e, leg) in enumerate(exprs)]


def stat(title, expr, unit="short", steps=None, description="", decimals=None, instant=True):
    """One number. `steps` colours it by value. A count that never happened has no series at all, so it reads 0, not "No data"."""
    d = {"unit": unit, "thresholds": thresholds(steps or [(None, "blue")]), "color": {"mode": "thresholds"}}
    if decimals is not None:
        d["decimals"] = decimals
    if "increase(" in expr or "status=\"5xx\"" in expr or expr.startswith("count(") or expr.startswith("sum(nk_bot_leader"):
        d["noValue"] = "0"
    t = targets([(expr, "")])
    for x in t:
        x["instant"], x["range"] = instant, not instant
    return {"type": "stat", "title": title, "description": description, "datasource": DS, "targets": t,
            "fieldConfig": {"defaults": d, "overrides": []},
            "options": {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False}, "colorMode": "background" if steps else "value",
                        "graphMode": "none", "textMode": "auto", "justifyMode": "center", "orientation": "auto"}}


def label_stat(title, expr, label, description=""):
    """Shows a label value as text (a version); one tile per value, so a rollout shows both versions."""
    t = targets([(expr, "")])
    t[0]["instant"], t[0]["range"], t[0]["format"] = True, False, "table"
    return {"type": "stat", "title": title, "description": description, "datasource": DS, "targets": t,
            "fieldConfig": {"defaults": {"thresholds": thresholds([(None, "blue")]), "color": {"mode": "fixed", "fixedColor": "blue"}}, "overrides": []},
            "options": {"reduceOptions": {"calcs": [], "fields": f"/^{label}$/", "values": True, "limit": 4}, "colorMode": "value", "graphMode": "none",
                        "textMode": "value", "justifyMode": "center", "orientation": "vertical"}}


def series(title, exprs, unit="short", description="", stack=False, steps=None, min_=None, max_=None, colors=None, decimals=None, bars=False):
    """A time series. `colors` maps a legend name to a colour; `steps` draws threshold lines.
    A single expression is named after the panel, so the legend never shows the query."""
    if isinstance(exprs, str):
        exprs = [(exprs, title)]
    custom = {"drawStyle": "bars" if bars else "line", "lineWidth": 1, "fillOpacity": 30 if (stack or bars) else 10, "showPoints": "never", "spanNulls": False,
              "stacking": {"mode": "normal" if (stack or bars) else "none", "group": "A"}, "lineInterpolation": "linear"}
    d = {"unit": unit, "custom": custom, "color": {"mode": "palette-classic"}}
    if steps:
        d["thresholds"] = thresholds(steps)
        custom["thresholdsStyle"] = {"mode": "line"}
    if min_ is not None:
        d["min"] = min_
    if max_ is not None:
        d["max"] = max_
    if decimals is not None:
        d["decimals"] = decimals
    overrides = [{"matcher": {"id": "byName", "options": name}, "properties": [{"id": "color", "value": {"mode": "fixed", "fixedColor": c}}]} for name, c in (colors or {}).items()]
    return {"type": "timeseries", "title": title, "description": description, "datasource": DS, "targets": targets(exprs),
            "fieldConfig": {"defaults": d, "overrides": overrides},
            "options": {"legend": {"displayMode": "list", "placement": "bottom", "showLegend": True, "calcs": []}, "tooltip": {"mode": "multi", "sort": "desc"}}}


def bars(title, expr, unit="short", description="", legend="{{route}}", steps=None):
    """A ranking at the end of the chosen time range: the biggest first."""
    t = targets([(expr, legend)])
    t[0]["instant"], t[0]["range"] = True, False
    return {"type": "bargauge", "title": title, "description": description, "datasource": DS, "targets": t,
            "fieldConfig": {"defaults": {"unit": unit, "min": 0, "thresholds": thresholds(steps or [(None, "blue")]), "color": {"mode": "thresholds"}}, "overrides": []},
            "options": {"displayMode": "gradient", "orientation": "horizontal", "showUnfilled": True, "valueMode": "color", "namePlacement": "auto",
                        "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False}}}


def table(title, expr, columns, description="", unit="short", steps=None):
    """A table of one instant query; `columns` renames the label columns."""
    t = targets([(expr, "")])
    t[0]["instant"], t[0]["range"], t[0]["format"] = True, False, "table"
    return {"type": "table", "title": title, "description": description, "datasource": DS, "targets": t,
            "fieldConfig": {"defaults": {"unit": unit, "custom": {"align": "auto", "cellOptions": {"type": "color-text" if steps else "auto"}},
                                         "thresholds": thresholds(steps or [(None, "text")]), "color": {"mode": "thresholds"}}, "overrides": []},
            "transformations": [{"id": "organize", "options": {"excludeByName": {"Time": True}, "renameByName": columns}}],
            "options": {"showHeader": True, "cellHeight": "sm", "footer": {"show": False}}}


def rate(metric, sel="", by="", window="$__rate_interval"):
    """sum by (...) (rate(metric{sel}[window]))"""
    body = f"rate({metric}{{{sel}}}[{window}])"
    return f"sum by ({by}) ({body})" if by else f"sum({body})"


def quantile(q, metric, sel="", by="", window="$__rate_interval"):
    keys = f"le, {by}" if by else "le"
    return f"histogram_quantile({q}, sum by ({keys}) (rate({metric}_bucket{{{sel}}}[{window}])))"


# ======================================================================================================================
# 1. Service health
# ======================================================================================================================
def service():
    b = Board("nk-service", "Notekeeper / Service health", "Is Core up, fast and error-free; realtime streams, database pool and process resources.", variables(core=True))
    user = f'{CORE}, listener="user"'
    server_errors, client_errors = f'{CORE}, status="5xx"', f'{CORE}, status="4xx"'
    b.row("At a glance")
    b.line(
        stat("Replicas up", f'count(nk_build_info{{{CORE}, component="core"}})', steps=[(None, BAD), (1, GOOD)], description="Core replicas that Prometheus reaches. Two or more is the recommended setup."),
        label_stat("Version", f'count by (version) (nk_build_info{{{CORE}, component="core"}})', "version", description="The version the replicas run; two show during a rollout."),
        stat("Requests / s", rate("nk_http_requests_total", user), unit="reqps", decimals=2, description="User API (the web app), all routes."),
        stat("Server errors", f'sum(rate(nk_http_requests_total{{{user}, status="5xx"}}[$__range])) / clamp_min(sum(rate(nk_http_requests_total{{{user}}}[$__range])), 1e-9)',
             unit="percentunit", decimals=2, steps=[(None, GOOD), (0.01, WARN), (0.05, BAD)], description="Share of user API requests that ended with a 5xx over the chosen time range. The alert fires above 5%."),
        stat("Latency p95", quantile(0.95, "nk_http_request_duration_seconds", user, window="$__range"), unit="s", decimals=3, steps=[(None, GOOD), (0.5, WARN), (2, BAD)],
             description="95% of user API requests were answered at least this fast over the chosen time range (realtime streams are not counted)."),
        stat("Open streams", f"sum(nk_realtime_streams{{{CORE}}})", description="Browsers currently receiving live updates."),
    h=4)

    b.row("Traffic")
    b.line(
        series("Requests per second, by listener", [(rate("nk_http_requests_total", CORE, "listener"), "{{listener}}")], unit="reqps",
               description="user = the web app, bot = chat bots, public = share links."),
        series("User API by status", [(rate("nk_http_requests_total", user, "status"), "{{status}}")], unit="reqps", stack=True,
               colors={"2xx": "green", "3xx": "blue", "4xx": "orange", "5xx": "red"}, description="Status class of every answer."),
    )
    b.line(
        series("User API latency", [(quantile(0.5, "nk_http_request_duration_seconds", user), "p50"), (quantile(0.95, "nk_http_request_duration_seconds", user), "p95"),
                                    (quantile(0.99, "nk_http_request_duration_seconds", user), "p99")], unit="s",
               description="How long requests take, without realtime streams. Uploads and downloads of big files are slow by nature."),
        bars("Slowest routes (p95)", f"topk(10, {quantile(0.95, 'nk_http_request_duration_seconds', user, 'route', '$__range')})", unit="s",
             description="The ten routes with the highest 95th percentile over the chosen time range.", steps=[(None, GOOD), (0.5, WARN), (2, BAD)]),
    )
    b.line(
        series("Server errors, by route", [(rate("nk_http_requests_total", server_errors, "listener, route"), "{{listener}} {{route}}")], unit="reqps",
               description="Which route fails. Empty is good."),
        series("Client errors, by route", [("topk(8, " + rate("nk_http_requests_total", client_errors, "listener, route") + ")", "{{listener}} {{route}}")], unit="reqps",
               description="4xx answers: mistakes of clients, or someone trying things. 401 after an expired session is normal; 499 is a browser that closed the connection before the answer."),
    )

    b.row("Realtime streams and database")
    b.line(
        series("Open realtime streams", [(f"sum by (instance) (nk_realtime_streams{{{CORE}}})", "{{instance}}")], description="Each open browser tab holds one. Each user may hold ten."),
        series("Streams refused", rate("nk_realtime_streams_refused_total", CORE), unit="ops", description="A user asked for an eleventh stream. It should stay at zero: a client that does not close its old streams causes it."),
        series("Database connections", [(f"sum(nk_db_pool_connections{{{CORE}}}) - sum(nk_db_pool_idle_connections{{{CORE}}})", "in use"),
                                        (f"sum(nk_db_pool_idle_connections{{{CORE}}})", "idle"), (f"sum(nk_db_pool_max_connections{{{CORE}}})", "pool size")],
               description="All replicas together. In use next to the pool size means requests are waiting.", stack=False),
        series("Wait for a database connection", [(f"sum(rate(nk_db_pool_acquire_wait_seconds_total{{{CORE}}}[$__rate_interval])) / clamp_min(sum(rate(nk_db_pool_acquires_total{{{CORE}}}[$__rate_interval])), 1e-9)", "average wait")],
               unit="s", steps=[(None, GOOD), (0.1, BAD)], description="How long a request waits for a connection on average. The alert fires above 0.1 s."),
    )

    b.row("Process")
    b.line(
        series("Memory (resident)", [(f"process_resident_memory_bytes{{{CORE}}}", "{{instance}}")], unit="bytes", description="Memory each Core replica holds. A line that climbs and never falls back points at a leak; compare it with the limit in the deployment."),
        series("CPU", [(f"rate(process_cpu_seconds_total{{{CORE}}}[$__rate_interval])", "{{instance}}")], unit="percentunit", description="Of one core."),
        series("Goroutines", [(f"go_goroutines{{{CORE}}}", "{{instance}}")], description="Concurrent tasks in each replica: about one per open request or stream. Steady growth means something is not finishing."),
        series("Go heap in use", [(f"go_memstats_heap_inuse_bytes{{{CORE}}}", "{{instance}}")], unit="bytes", description="Memory the Go runtime holds for live objects; the part of the resident memory the program itself is using."),
    )
    return b


# ======================================================================================================================
# 2. Messages and reminders
# ======================================================================================================================
def pipeline():
    b = Board("nk-pipeline", "Notekeeper / Messages and reminders", "What arrives from chats, where it goes, and whether reminders leave on time: ingest, grouping, reminders, outbox and background tasks.", variables(core=True))
    b.row("At a glance")
    b.line(
        stat("Chat messages", f"round(sum(increase(nk_ingest_events_total{{{CORE}}}[$__range])))", description="Events from chats over the chosen time range."),
        stat("Reminders fired", f"round(sum(increase(nk_reminders_fired_total{{{CORE}}}[$__range])))", description="Over the chosen time range."),
        stat("Reminders late", f'round(sum(increase(nk_reminders_fired_total{{{CORE}, late="true"}}[$__range])))', steps=[(None, GOOD), (1, WARN)], description="Fired more than the allowed time after they were due."),
        stat("Lag p95", quantile(0.95, "nk_reminder_lag_seconds", CORE, window="$__range"), unit="s", decimals=1, steps=[(None, GOOD), (30, WARN), (60, BAD)],
             description="Seconds between the due time and firing. The target is under a minute (CORE-R5)."),
        stat("Waiting for bot", f'sum(nk_outbox_items{{{CORE}, state=~"queued|claimed"}})', steps=[(None, GOOD), (10, WARN), (50, BAD)], description="Reminders and notices that no bot has delivered yet."),
        stat("Oldest waiting", f"max(nk_outbox_oldest_wait_seconds{{{CORE}}})", unit="s", steps=[(None, GOOD), (60, WARN), (300, BAD)], description="How long the item that has waited longest has waited. The alert fires above five minutes."),
    h=4)

    b.row("Chat messages")
    b.line(
        series("Events by kind and result", [(rate("nk_ingest_events_total", CORE, "kind, result"), "{{kind}} {{result}}")], unit="ops", stack=True,
               description="What the bots sent to Core and what came of it. Rejected should be rare."),
        series("Where new messages went", [(rate("nk_grouping_decisions_total", CORE, "reason"), "{{reason}}")], unit="ops", stack=True,
               description="Why a message started a note or joined one (GRP-8)."),
    )

    b.row("Reminders")
    b.line(
        series("Reminders fired", [(rate("nk_reminders_fired_total", CORE, "late"), "late={{late}}")], unit="ops", stack=True,
               colors={"late=false": "green", "late=true": "red"}, description="On time or late."),
        series("Reminder lag", [(quantile(0.5, "nk_reminder_lag_seconds", CORE), "p50"), (quantile(0.95, "nk_reminder_lag_seconds", CORE), "p95"), (quantile(0.99, "nk_reminder_lag_seconds", CORE), "p99")],
               unit="s", steps=[(None, GOOD), (60, BAD)], description="Seconds from due time to firing. Empty when nothing fired."),
    )

    b.row("Outbox (what Core asks the bots to send)")
    b.line(
        series("Items by state", [(f"sum by (state) (nk_outbox_items{{{CORE}}})", "{{state}}")], stack=True,
               colors={"queued": "yellow", "claimed": "orange", "delivered": "green", "failed": "red", "expired": "purple"}, description="Delivered and failed are history kept for 30 days; queued and claimed are waiting."),
        series("Oldest waiting item", f"max(nk_outbox_oldest_wait_seconds{{{CORE}}})", unit="s", steps=[(None, GOOD), (300, BAD)], min_=0,
               description="Rises while no bot delivers. Zero when nothing waits."),
        series("What the bots reported", [(rate("nk_outbox_results_total", CORE, "kind, result"), "{{kind}} {{result}}")], unit="ops", stack=True, description="Outcome of each delivery attempt."),
    )

    b.row("Background tasks")
    b.line(
        table("Time since each task last succeeded", f"time() - max by (task) (nk_job_last_success_timestamp_seconds{{{CORE}}})", {"task": "Task", "Value": "Since last success"},
              unit="s", steps=[(None, GOOD), (900, WARN), (3600, BAD)],
              description="fire_due_reminders and expire_outbox run every few seconds; the others every few minutes to a day. A task that stopped is not a failure, so watch this."),
        series("Runs by result", [(rate("nk_job_runs_total", CORE, "task, result"), "{{task}} {{result}}")], unit="ops", stack=True, description="Every run of every task."),
        series("Run time p95", [(quantile(0.95, "nk_job_duration_seconds", CORE, "task"), "{{task}}")], unit="s", description="How long a run takes."),
        series("Failures", [(rate("nk_job_failures_total", CORE, "task"), "{{task}}")], unit="ops", colors={}, description="Runs that returned an error. Empty is good."),
    )
    return b


# ======================================================================================================================
# 3. Matrix bot
# ======================================================================================================================
def matrix_bot():
    b = Board("nk-bot", "Notekeeper / Matrix bot", "The chat bridge: is the bot syncing, how old are messages when it handles them, what it did with them, and how it talks to Core.", variables(bot=True))
    b.row("At a glance")
    b.line(
        stat("Replicas up", f'count(nk_build_info{{{BOT}, component="matrix-bot"}})', steps=[(None, BAD), (1, GOOD)], description="Bot replicas that Prometheus reaches. Two are recommended: one syncs, the other waits."),
        stat("Active replica", f"sum(nk_bot_leader{{{BOT}}})", steps=[(None, BAD), (1, GOOD)], description="Exactly one replica holds the lock and syncs the account. Zero: no one is syncing."),
        label_stat("Version", f'count by (version) (nk_build_info{{{BOT}, component="matrix-bot"}})', "version", description="The version the bot replicas run; two show during a rollout."),
        stat("Since last sync", f"time() - max(nk_bot_last_sync_timestamp_seconds{{{BOT}}})", unit="s", decimals=0, steps=[(None, GOOD), (120, WARN), (600, BAD)],
             description="Seconds since the bot finished handling a sync response. The stalled-bot alert fires above ten minutes."),
        stat("Undecryptable", f"round(sum(increase(nk_bot_decrypt_failures_total{{{BOT}}}[$__range])))", steps=[(None, GOOD), (1, WARN)], description="Messages the bot could not read (missing encryption keys) over the chosen time range."),
        stat("Message age p95", quantile(0.95, "nk_bot_event_age_seconds", BOT, window="$__range"), unit="s", decimals=1, steps=[(None, GOOD), (15, WARN), (300, BAD)],
             description="How long after being sent the bot handled 95% of messages."),
    h=4)

    b.row("Chat to Notekeeper")
    b.line(
        series("Events by result", [(rate("nk_bot_events_total", BOT, "result"), "{{result}}")], unit="ops", stack=True, description="What became of each chat event: created, ignored, command, refused, ..."),
        series("Message age when handled", [(quantile(0.5, "nk_bot_event_age_seconds", BOT), "p50"), (quantile(0.95, "nk_bot_event_age_seconds", BOT), "p95"), (quantile(0.99, "nk_bot_event_age_seconds", BOT), "p99")], unit="s",
               description="The delay between sending a message and the bot handling it. After downtime it shows the catch-up."),
    )
    b.line(
        series("Files from chat", [(rate("nk_bot_attachments_total", BOT, "result"), "{{result}}")], unit="ops", stack=True, description="Files moved from chat to Core: ok, or the reason it failed."),
        series("Decryption failures", rate("nk_bot_decrypt_failures_total", BOT), unit="ops", description="Events the bot could not read. A burst after a restart means keys did not arrive."),
        series("Gaps in the timeline", [(rate("nk_bot_sync_gaps_total", BOT, "outcome"), "{{outcome}}")], unit="ops", stack=True, description="The homeserver left messages out of a room's timeline; filled means the bot fetched them, truncated means it could not."),
    )

    b.row("Notekeeper to chat")
    b.line(
        series("Deliveries", [(rate("nk_bot_outbox_total", BOT, "kind, result"), "{{kind}} {{result}}")], unit="ops", stack=True, description="Reminders and notices sent to chats."),
        series("Could not ask Core for work", rate("nk_bot_outbox_claim_failures_total", BOT), unit="ops", steps=[(None, GOOD), (0.01, BAD)], description="While this is above zero nothing is delivered."),
        series("Calls to Core that were retried", [(rate("nk_bot_core_retries_total", BOT, "operation"), "{{operation}}")], unit="ops", stack=True, description="Core slow, restarting or unreachable."),
    )

    b.row("Process")
    b.line(
        series("Memory (resident)", [(f"process_resident_memory_bytes{{{BOT}}}", "{{instance}}")], unit="bytes", description="Memory each bot replica holds. The bot keeps the encryption state in memory, so it grows with the number of chats; a steady climb is a leak."),
        series("CPU", [(f"rate(process_cpu_seconds_total{{{BOT}}}[$__rate_interval])", "{{instance}}")], unit="percentunit", description="Of one core."),
        series("Goroutines", [(f"go_goroutines{{{BOT}}}", "{{instance}}")], description="Concurrent tasks in each bot replica. Steady growth means something is not finishing."),
        series("Sync age per replica", [(f"time() - nk_bot_last_sync_timestamp_seconds{{{BOT}}} > 0", "{{instance}}")], unit="s", description="A standby replica shows nothing."),
    )
    return b


# ======================================================================================================================
# 4. Security and abuse
# ======================================================================================================================
def security():
    b = Board("nk-security", "Notekeeper / Security and abuse", "Who is trying what: sign-in failures, replayed tokens, bad bot keys, share-link probing and rate limits. These are counts; no names or content are ever recorded.", variables(core=True))
    b.row("At a glance (chosen time range)")
    b.line(
        stat("Failed sign-ins", f'round(sum(increase(nk_auth_events_total{{{CORE}, event="failed"}}[$__range])))', steps=[(None, GOOD), (10, WARN), (30, BAD)], description="Wrong passwords. A few are people mistyping; dozens in a short time are someone guessing."),
        stat("Throttled", f'round(sum(increase(nk_auth_events_total{{{CORE}, event="throttled"}}[$__range])))', steps=[(None, GOOD), (1, WARN)], description="Sign-ins refused because of too many failures."),
        stat("Replayed tokens", f'round(sum(increase(nk_auth_events_total{{{CORE}, event="refresh_reuse"}}[$__range])))', steps=[(None, GOOD), (1, WARN), (5, BAD)],
             description="An old token was used again and the session was ended: a stolen token, or a client that renews its session wrongly."),
        stat("Bot rejections", f"round(sum(increase(nk_bot_requests_rejected_total{{{CORE}}}[$__range])))", steps=[(None, GOOD), (1, WARN), (20, BAD)], description="Requests to the bot API with a bad or unauthorised key. Any at all is worth a look: it may be a leaked or guessed key."),
        stat("Unknown links", f'round(sum(increase(nk_share_requests_total{{{CORE}, result="not_found"}}[$__range])))', steps=[(None, GOOD), (20, WARN), (100, BAD)], description="Requests for share links that do not exist, which includes expired ones. Many in a short time means someone is trying tokens."),
        stat("Rate-limited", f"round(sum(increase(nk_rate_limited_total{{{CORE}}}[$__range])))", steps=[(None, GOOD), (50, WARN), (200, BAD)], description="Requests refused by a rate limit. A busy legitimate client can cause a few; a flood is abuse or a client stuck in a retry loop."),
    h=4)

    b.row("Sign-in")
    b.line(
        series("Sign-ins by outcome", [(rate("nk_auth_events_total", CORE, "event"), "{{event}}")], unit="ops", stack=True,
               colors={"succeeded": "green", "failed": "orange", "throttled": "red", "refresh_reuse": "purple"}, description="succeeded, failed, throttled; refresh_reuse is a replayed session token."),
        series("Share of sign-ins that fail", f'sum(rate(nk_auth_events_total{{{CORE}, event=~"failed|throttled"}}[$__rate_interval])) / clamp_min(sum(rate(nk_auth_events_total{{{CORE}, event=~"succeeded|failed|throttled"}}[$__rate_interval])), 1e-9)',
               unit="percentunit", min_=0, max_=1, description="A password-guessing run shows as a jump to most of the attempts."),
    )

    b.row("Bots and share links")
    b.line(
        series("Rejected bot requests, by reason", [(rate("nk_bot_requests_rejected_total", CORE, "reason"), "{{reason}}")], unit="ops", stack=True,
               description="bad_key: a key that does not exist. wrong_scope: a real key used where it may not be. refused: refused before doing anything."),
        series("Share link requests", [(rate("nk_share_requests_total", CORE, "result"), "{{result}}")], unit="ops", stack=True,
               colors={"ok": "green", "not_found": "orange", "limited": "red"}, description="not_found rising means someone is trying tokens."),
    )

    b.row("Limits")
    b.line(
        series("Rate-limited requests, by scope", [(rate("nk_rate_limited_total", CORE, "scope"), "{{scope}}")], unit="ops", stack=True, description="Requests refused by a rate limit."),
        series("Streams refused", rate("nk_realtime_streams_refused_total", CORE), unit="ops", description="A user asked for more live-update streams than allowed."),
        bars("Routes with the most 4xx answers", f'topk(10, round(sum by (listener, route) (increase(nk_http_requests_total{{{CORE}, status="4xx"}}[$__range]))) > 0)', unit="short", legend="{{listener}} {{route}}",
             description="Over the chosen time range. A route far above the others is being hammered."),
    )
    return b


def main():
    for build in (service, pipeline, matrix_bot, security):
        b = build()
        (OUT / f"{b.uid}.json").write_text(json.dumps(b.json(), indent=2, sort_keys=False) + "\n")


if __name__ == "__main__":
    main()
