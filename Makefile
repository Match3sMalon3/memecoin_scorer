.PHONY: build test fmt vet \
        run-ingestor run-dashboard run-dashboard-live run-scorer \
        run-production setup-live-env prove-go-live \
        prove-ingress prove-events \
        set-helius-webhook webhook-env-check \
        healthcheck-ingestor healthcheck-dashboard healthcheck \
        smoke-webhook smoke-cluster show-snapshots validate-live \
        db-migrate env-check \
        clean-stop clean-start clean-start-debug _start_services \
        status tail-ingestor tail-dashboard reset-state

# ---------------------------------------------------------------------------
# Source .env if it exists.  Callers can override individual vars on the
# command line as usual:  LIVE_MODE=1 make run-dashboard
#
# PUBLIC_BASE_URL is a per-session tunnel URL (changes every cloudflared run).
# It must NOT be overwritten by .env.  We capture the calling-shell value
# before the include can clobber it and restore it afterwards.
# ---------------------------------------------------------------------------
_ENV_PUBLIC_BASE_URL := $(PUBLIC_BASE_URL)
-include .env
export
ifneq ($(_ENV_PUBLIC_BASE_URL),)
  PUBLIC_BASE_URL := $(_ENV_PUBLIC_BASE_URL)
endif

# ---------------------------------------------------------------------------
# Build / test / lint
# ---------------------------------------------------------------------------

build:
	go build ./...

test:
	go test ./...

test-race:
	go test -race ./...

fmt:
	gofmt -w .

vet:
	go vet ./...

# ---------------------------------------------------------------------------
# Run services (foreground, for development)
# ---------------------------------------------------------------------------

run-ingestor:
	go run ./cmd/ingestor

run-dashboard:
	go run ./cmd/dashboard

run-dashboard-live:
	LIVE_MODE=1 INGESTOR_URL=$${INGESTOR_URL:-http://localhost:8080} go run ./cmd/dashboard

# run-production: build and start ingestor + dashboard with clustering and DB required.
# Requires HELIUS_API_KEY (or FUNDER_MAP_PATH) and DATABASE_URL to be set.
run-production: clean-stop
	@mkdir -p logs .pids
	@[ -n "$${HELIUS_API_KEY}" ] || [ -n "$${FUNDER_MAP_PATH}" ] || \
		(echo "ERROR: HELIUS_API_KEY or FUNDER_MAP_PATH required for production clustering" && exit 1)
	@echo "Building production binaries..."
	@go build -o .pids/ingestor_bin ./cmd/ingestor
	@go build -o .pids/dashboard_bin ./cmd/dashboard
	@[ -z "$${DATABASE_URL}" ] || $(MAKE) db-migrate
	@echo "Starting ingestor (production) on :$${PORT:-8080}..."
	@./.pids/ingestor_bin >> logs/ingestor.log 2>&1 & echo $$! > .pids/ingestor.pid
	@echo "Starting dashboard (production) on :8090..."
	@LIVE_MODE=1 INGESTOR_URL=$${INGESTOR_URL:-http://localhost:8080} \
		./.pids/dashboard_bin >> logs/dashboard.log 2>&1 & echo $$! > .pids/dashboard.pid
	@sleep 1
	@make healthcheck

# setup-live-env: safely bootstrap the local .env with the Helius API key.
# Prompts the operator with silent input — key is never echoed or logged.
setup-live-env:
	@scripts/setup-live-env.sh

# prove-ingress: verify the ingress poller is configured and has connected to Helius.
# Requires the stack to be running (make clean-start first).
# Does NOT wait for events — use prove-events for that.
prove-ingress:
	@echo "=== prove-ingress: checking ingress status ==="
	@HEALTH=$$(curl -sf --max-time 5 http://localhost:$(_IPORT)/healthz) || \
		{ echo "FAIL: ingestor /healthz unreachable on :$(_IPORT)"; exit 1; }; \
	CONFIGURED=$$(echo "$$HEALTH" | python3 -c \
		"import sys,json; print(json.load(sys.stdin).get('ingress',{}).get('configured', False))" 2>/dev/null); \
	CONNECTED=$$(echo "$$HEALTH" | python3 -c \
		"import sys,json; print(json.load(sys.stdin).get('ingress',{}).get('connected', False))" 2>/dev/null); \
	EVENTS=$$(echo "$$HEALTH" | python3 -c \
		"import sys,json; print(json.load(sys.stdin).get('ingress',{}).get('events_total', 0))" 2>/dev/null); \
	PROGRAMS=$$(echo "$$HEALTH" | python3 -c \
		"import sys,json; print(', '.join(json.load(sys.stdin).get('ingress',{}).get('programs', [])))" 2>/dev/null); \
	echo "  configured : $$CONFIGURED"; \
	echo "  connected  : $$CONNECTED"; \
	echo "  events     : $$EVENTS"; \
	echo "  programs   : $$PROGRAMS"; \
	[ "$$CONFIGURED" = "True" ] || { echo "FAIL: ingress not configured — HELIUS_API_KEY must be set"; exit 1; }; \
	[ "$$CONNECTED" = "True" ] || { echo "FAIL: ingress not connected — check HELIUS_API_KEY validity and network"; exit 1; }; \
	echo "PASS: ingress configured=true connected=true"

# prove-events: wait up to 30s for live swap events to arrive and create snapshots.
# Requires the stack to be running and ingress to be connected (run prove-ingress first).
prove-events:
	@echo "=== prove-events: waiting for live events (up to 30s) ==="
	@i=0; \
	while [ "$$i" -lt 6 ]; do \
	  SNAPS=$$(curl -sf "http://localhost:$(_IPORT)/api/snapshots?min_buyers=1&since_minutes=30&limit=5" \
	    | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null || echo 0); \
	  [ "$$SNAPS" -gt "0" ] && break; \
	  echo "  waiting… ($$(( (i+1)*5 ))s elapsed, $$SNAPS snapshots)"; \
	  sleep 5; i=$$((i+1)); \
	done; \
	SNAPS=$$(curl -sf "http://localhost:$(_IPORT)/api/snapshots?min_buyers=1&since_minutes=30&limit=5" \
	  | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null || echo 0); \
	echo "  snapshots found: $$SNAPS"; \
	[ "$$SNAPS" -gt "0" ] || { echo "FAIL: no snapshots after 30s — ingress connected but no SWAP events seen"; exit 1; }; \
	echo "PASS: $$SNAPS live token snapshots present"

# set-helius-webhook: OPTIONAL — registers a Helius push webhook for advanced use.
#
# The PRIMARY ingress path is the built-in poll poller (HELIUS_API_KEY only).
# You do NOT need this for broad market discovery.
#
# Only register a webhook if you want Helius to push events directly (requires
# a publicly reachable URL via cloudflared tunnel or deployed server).
#
# Operator flow:
#   1. make clean-start
#   2. cloudflared tunnel --url http://localhost:8080
#   3. export PUBLIC_BASE_URL=https://<assigned>.trycloudflare.com
#   4. make set-helius-webhook
#
# HELIUS_API_KEY and HELIUS_ACCOUNT_ADDRESSES are loaded from .env automatically.
# PUBLIC_BASE_URL must be exported in the calling shell (it changes each tunnel session).
# Set HELIUS_WEBHOOK_ID to update an existing webhook instead of creating a new one.
set-helius-webhook:
	@scripts/set-helius-webhook.sh

# webhook-env-check: confirm variables are in scope for the optional push webhook.
# The primary ingress (polling) requires only HELIUS_API_KEY — no webhook needed.
webhook-env-check:
	@echo "=== webhook-env-check (optional push webhook) ==="
	@[ -f .env ] \
		&& echo "  .env                      : found" \
		|| echo "  .env                      : NOT found — run make setup-live-env"
	@[ -n "$${PUBLIC_BASE_URL}" ] \
		&& echo "  PUBLIC_BASE_URL           : $${PUBLIC_BASE_URL}" \
		|| echo "  PUBLIC_BASE_URL           : NOT SET — export PUBLIC_BASE_URL=https://....trycloudflare.com"
	@[ -n "$${HELIUS_API_KEY}" ] \
		&& echo "  HELIUS_API_KEY            : set (length=$${#HELIUS_API_KEY})" \
		|| echo "  HELIUS_API_KEY            : NOT SET — run make setup-live-env"
	@[ -n "$${HELIUS_ACCOUNT_ADDRESSES}" ] \
		&& echo "  HELIUS_ACCOUNT_ADDRESSES  : $${HELIUS_ACCOUNT_ADDRESSES}" \
		|| echo "  HELIUS_ACCOUNT_ADDRESSES  : NOT SET (required for push webhook only)"

# prove-go-live: pre-flight check that strict live mode is ready.
# Verifies .env exists, HELIUS_API_KEY is present and not the placeholder,
# then starts services and confirms healthcheck shows backend=helius, healthy=true.
PLACEHOLDER := __PASTE_YOUR_HELIUS_API_KEY_HERE__
prove-go-live:
	@echo "=== prove-go-live: pre-flight checks ==="
	@[ -f .env ] || { echo "FAIL: .env does not exist. Run: make setup-live-env"; exit 1; }
	@KEY=$$(grep "^HELIUS_API_KEY=" .env 2>/dev/null | head -1 | cut -d'=' -f2-); \
	scripts/validate-helius-key.sh "$$KEY"
	@echo ""
	@echo "=== prove-go-live: starting services ==="
	@$(MAKE) clean-start
	@sleep 2
	@echo ""
	@echo "=== prove-go-live: healthcheck ==="
	@HEALTH=$$(curl -sf http://localhost:$${PORT:-8080}/healthz) || \
		(echo "FAIL: ingestor /healthz unreachable" && exit 1); \
	echo "$$HEALTH" | python3 -m json.tool; \
	BACKEND=$$(echo "$$HEALTH" | python3 -c "import sys,json; print(json.load(sys.stdin).get('clustering',{}).get('backend','?'))" 2>/dev/null); \
	STATUS=$$(echo "$$HEALTH" | python3 -c "import sys,json; print(json.load(sys.stdin).get('clustering',{}).get('status','?'))" 2>/dev/null); \
	HEALTHY=$$(echo "$$HEALTH" | python3 -c "import sys,json; print(json.load(sys.stdin).get('clustering',{}).get('healthy','?'))" 2>/dev/null); \
	echo ""; \
	echo "  backend = $$BACKEND"; \
	echo "  status  = $$STATUS"; \
	echo "  healthy = $$HEALTHY"; \
	echo ""; \
	[ "$$BACKEND" = "helius" ] || (echo "FAIL: backend=$$BACKEND, want helius. Check HELIUS_API_KEY." && exit 1); \
	[ "$$HEALTHY" = "True" ] || (echo "FAIL: healthy=$$HEALTHY. Helius resolver degraded — check key validity and network." && exit 1); \
	[ "$$STATUS" = "healthy" ] || (echo "FAIL: status=$$STATUS. Want healthy." && exit 1); \
	echo "PASS: clustering backend=$$BACKEND healthy=true"
	@echo ""
	@echo "=== prove-go-live: verifying ingress poller ==="
	@$(MAKE) prove-ingress
	@echo ""
	@echo "PASS: strict live mode confirmed — clustering=helius, ingress=polling, no accountAddresses required"

run-scorer:
	go run ./cmd/scorer \
		-input $(INPUT) \
		-config config/scoring_config.yaml \
		-output-csv results_scored.csv \
		-output-json results_summary.json

# ---------------------------------------------------------------------------
# Healthchecks
# ---------------------------------------------------------------------------

INGESTOR_PORT  ?= 8080
DASHBOARD_PORT ?= 8090

healthcheck-ingestor:
	@echo "--- ingestor /healthz ---"
	@HEALTH=$$(curl -sf http://localhost:$(INGESTOR_PORT)/healthz) || \
		{ echo "FAIL: ingestor not reachable on :$(INGESTOR_PORT)"; exit 1; }; \
	echo "$$HEALTH" | python3 -m json.tool; \
	CLUSTER_STATUS=$$(echo "$$HEALTH" | python3 -c "import sys,json; h=json.load(sys.stdin); c=h.get('clustering',{}); print('backend={} status={} buy_ready_allowed={}'.format(c.get('backend','?'), c.get('status','?'), c.get('buy_ready_allowed','?')))" 2>/dev/null); \
	echo ""; \
	echo "clustering: $$CLUSTER_STATUS"; \
	CLUSTER_HEALTHY=$$(echo "$$HEALTH" | python3 -c "import sys,json; h=json.load(sys.stdin); print('yes' if h.get('clustering',{}).get('healthy',False) else 'no')" 2>/dev/null); \
	[ "$$CLUSTER_HEALTHY" = "yes" ] && echo "clustering: OK" || \
		(echo "FAIL: clustering is DEGRADED — BUY/READY disabled until HELIUS_API_KEY or FUNDER_MAP_PATH is set" && exit 2)
	@echo ""
	@echo "--- recent snapshots (limit=5) ---"
	@curl -sf "http://localhost:$(INGESTOR_PORT)/api/snapshots?min_buyers=1&since_minutes=30&limit=5" \
		| python3 -m json.tool 2>/dev/null | head -60 || echo "(no snapshots yet)"

healthcheck-dashboard:
	@echo "--- dashboard /healthz ---"
	@curl -sf http://localhost:$(DASHBOARD_PORT)/healthz | python3 -m json.tool || \
		(echo "ERROR: dashboard not reachable on :$(DASHBOARD_PORT)" && exit 1)
	@echo ""
	@echo "--- dashboard /api/config ---"
	@curl -sf http://localhost:$(DASHBOARD_PORT)/api/config | python3 -m json.tool

healthcheck: healthcheck-ingestor healthcheck-dashboard

# ---------------------------------------------------------------------------
# Local smoke tests
# ---------------------------------------------------------------------------

smoke-webhook:
	@scripts/smoke-webhook.sh

# smoke-cluster: proves raw buyers collapse into fewer effective buyers via
# in-process StaticResolver tests.  No running ingestor required.
smoke-cluster:
	@scripts/smoke-cluster.sh

show-snapshots:
	@curl -sf "http://localhost:$(_IPORT)/api/snapshots?min_buyers=1&since_minutes=60&limit=20" \
		| python3 -m json.tool || echo "(ingestor not running or no data yet)"

# ---------------------------------------------------------------------------
# End-to-end validation
# ---------------------------------------------------------------------------

validate-live:
	@scripts/validate-live.sh

# ---------------------------------------------------------------------------
# Environment helpers
# ---------------------------------------------------------------------------

# db-migrate: apply schema.sql to DATABASE_URL.
db-migrate:
	@[ -n "$${DATABASE_URL}" ] || (echo "ERROR: DATABASE_URL not set" && exit 1)
	@echo "Applying schema migrations to $$DATABASE_URL ..."
	@psql "$$DATABASE_URL" -f internal/db/schema.sql
	@echo "db-migrate done."

env-check:
	@echo "DATABASE_URL                           = $${DATABASE_URL:-(not set — persistence disabled)}"
	@echo "PORT                                   = $${PORT:-8080 (default)}"
	@echo "HELIUS_WEBHOOK_SECRET                  = $${HELIUS_WEBHOOK_SECRET:-(not set — auth disabled)}"
	@echo "LIVE_MODE                              = $${LIVE_MODE:-0 (offline)}"
	@echo "INGESTOR_URL                           = $${INGESTOR_URL:-http://localhost:8080 (default)}"
	@echo "REFRESH_INTERVAL_SEC                   = $${REFRESH_INTERVAL_SEC:-10 (default)}"
	@echo "TRADE_SIZE_SOL                         = $${TRADE_SIZE_SOL:-1.0 (default)}"
	@echo "MAX_ESTIMATED_IMPACT_PCT               = $${MAX_ESTIMATED_IMPACT_PCT:-15.0 (default)}"
	@echo "MAX_SIGNAL_AGE_MINUTES_BUYREADY        = $${MAX_SIGNAL_AGE_MINUTES_BUYREADY:-5 (default)}"
	@echo "MAX_SIGNAL_AGE_MINUTES_WATCH           = $${MAX_SIGNAL_AGE_MINUTES_WATCH:-15 (default)}"
	@echo "MIN_TOKEN_AGE_SECONDS_FOR_BUY          = $${MIN_TOKEN_AGE_SECONDS_FOR_BUY:-90 (default)}"
	@echo "MIN_EFFECTIVE_BUYERS_1M_FOR_CONFIDENT_BUY = $${MIN_EFFECTIVE_BUYERS_1M_FOR_CONFIDENT_BUY:-3 (default)}"
	@echo "MIN_TOTAL_EVENTS_FOR_CONFIDENCE        = $${MIN_TOTAL_EVENTS_FOR_CONFIDENCE:-3 (default)}"
	@echo "ENABLE_LOCAL_ADMIN                     = $${ENABLE_LOCAL_ADMIN:-0 (admin disabled)}"
	@echo "PUBLIC_BASE_URL                        = $${PUBLIC_BASE_URL:-(not set — tunnel not configured)}"

# ---------------------------------------------------------------------------
# Module 6F — Operator lifecycle
#
# Port assignment — explicit and non-ambiguous:
#   INGESTOR_PORT  (default 8080) — only the ingestor reads this
#   DASHBOARD_PORT (default 8090) — only the dashboard reads this
#   PORT is NOT passed to either service by the Makefile; it remains available
#   for callers who set it externally (legacy support).
# ---------------------------------------------------------------------------

_IPORT ?= 8080
_DPORT ?= 8090

# _wait_healthz SERVICE URL MAXWAIT_SECS
# Polls URL/healthz every 0.5 s until it returns 200 or MAXWAIT_SECS is exceeded.
# Prints the last log tail on failure.
define _wait_healthz
	@{ \
	  svc="$(1)"; url="$(2)"; log="$(3)"; limit="$(4)"; \
	  i=0; \
	  while [ "$$i" -lt "$$limit" ]; do \
	    if curl -sf --max-time 1 "$$url/healthz" > /dev/null 2>&1; then \
	      echo "  $$svc  UP  ($$url)"; break; \
	    fi; \
	    sleep 0.5; i=$$((i+1)); \
	  done; \
	  if [ "$$i" -ge "$$limit" ]; then \
	    echo "FAIL: $$svc did not respond within $$((limit/2))s — last log:"; \
	    tail -20 "$$log" 2>/dev/null || echo "  (no log at $$log)"; \
	    $(MAKE) clean-stop; exit 1; \
	  fi; \
	}
endef

# _check_port PORT SERVICE
# Fails hard if the port is occupied by a process we do not own (not in .pids/).
define _check_port
	@{ \
	  port="$(1)"; svc="$(2)"; \
	  listener=$$(lsof -iTCP:$$port -sTCP:LISTEN -n -P 2>/dev/null | awk 'NR>1 {print; exit}'); \
	  if [ -z "$$listener" ]; then exit 0; fi; \
	  echo "FAIL: port $$port is occupied before starting $$svc"; \
	  echo "LISTENER: $$listener"; \
	  exit 1; \
	}
endef

# clean-stop: kill ingestor + dashboard (own pids first, then sweep stray processes).
# Always succeeds — does not fail if processes are already absent.
clean-stop:
	@{ \
	  if [ -f .pids/ingestor.pid ]; then \
	    pid=$$(cat .pids/ingestor.pid 2>/dev/null); \
	    kill "$$pid" 2>/dev/null && echo "  stopped ingestor  (pid $$pid)" || true; \
	    rm -f .pids/ingestor.pid; \
	  fi; \
	  if [ -f .pids/dashboard.pid ]; then \
	    pid=$$(cat .pids/dashboard.pid 2>/dev/null); \
	    kill "$$pid" 2>/dev/null && echo "  stopped dashboard (pid $$pid)" || true; \
	    rm -f .pids/dashboard.pid; \
	  fi; \
	  pkill -f 'ingestor_bin'  2>/dev/null || true; \
	  pkill -f 'dashboard_bin' 2>/dev/null || true; \
	  pkill -f '/cmd/ingestor' 2>/dev/null || true; \
	  pkill -f '/cmd/dashboard' 2>/dev/null || true; \
	  pkill -f cloudflared 2>/dev/null || true; \
	  echo "clean-stop done."; \
	}

# _start_services: internal recipe used by both clean-start and clean-start-debug.
# Caller must have already run the pre-flight checks it needs.
# Expects: IPORT, DPORT, INGESTOR_EXTRA_ENV set by the calling target.
_start_services:
	@mkdir -p logs .pids
	@echo "Building binaries..."
	@go build -o .pids/ingestor_bin  ./cmd/ingestor
	@go build -o .pids/dashboard_bin ./cmd/dashboard
	@echo "Killing stale project root binaries..."
	@pkill -9 -f "/Users/ddff/Downloads/memecoin_scorer/dashboard" || true
	@pkill -9 -f "/Users/ddff/Downloads/memecoin_scorer/ingestor" || true
	@echo "Checking port $(_IPORT) (ingestor)..."
	$(call _check_port,$(_IPORT),ingestor)
	@echo "Checking port $(_DPORT) (dashboard)..."
	$(call _check_port,$(_DPORT),dashboard)
	@echo "Starting ingestor  on :$(_IPORT)..."
	@PORT= INGESTOR_PORT=$(_IPORT) $(INGESTOR_EXTRA_ENV) \
		./.pids/ingestor_bin >> logs/ingestor.log 2>&1 & \
		BGPID=$$!; \
		sleep 0.3; \
		if ! kill -0 $$BGPID 2>/dev/null; then \
		  echo "FAIL: ingestor exited immediately — last log:"; \
		  tail -20 logs/ingestor.log; exit 1; \
		fi; \
		echo $$BGPID > .pids/ingestor.pid
	@echo "Starting dashboard on :$(_DPORT)..."
	@PORT= DASHBOARD_PORT=$(_DPORT) LIVE_MODE=1 \
		INGESTOR_URL=$${INGESTOR_URL:-http://localhost:$(_IPORT)} \
		./.pids/dashboard_bin >> logs/dashboard.log 2>&1 & \
		BGPID=$$!; \
		sleep 0.3; \
		if ! kill -0 $$BGPID 2>/dev/null; then \
		  echo "FAIL: dashboard exited immediately — last log:"; \
		  tail -20 logs/dashboard.log; exit 1; \
		fi; \
		echo $$BGPID > .pids/dashboard.pid
	@echo "Waiting for ingestor  /healthz..."
	$(call _wait_healthz,ingestor,http://localhost:$(_IPORT),logs/ingestor.log,20)
	@echo "Waiting for dashboard /healthz..."
	$(call _wait_healthz,dashboard,http://localhost:$(_DPORT),logs/dashboard.log,20)
	@{ \
	  ingestor_listener=$$(lsof -tiTCP:$(_IPORT) -sTCP:LISTEN -n -P 2>/dev/null | head -1); \
	  dashboard_listener=$$(lsof -tiTCP:$(_DPORT) -sTCP:LISTEN -n -P 2>/dev/null | head -1); \
	  ingestor_pid=$$(cat .pids/ingestor.pid 2>/dev/null || true); \
	  dashboard_pid=$$(cat .pids/dashboard.pid 2>/dev/null || true); \
	  echo "  listener PID on $(_IPORT): $$ingestor_listener"; \
	  echo "  listener PID on $(_DPORT): $$dashboard_listener"; \
	  echo "  .pids/ingestor.pid: $$ingestor_pid"; \
	  echo "  .pids/dashboard.pid: $$dashboard_pid"; \
	  if [ "$$ingestor_listener" != "$$ingestor_pid" ]; then \
	    echo "FAIL: ingestor listener PID does not match .pids/ingestor.pid"; \
	    exit 1; \
	  fi; \
	  if [ "$$dashboard_listener" != "$$dashboard_pid" ]; then \
	    echo "FAIL: dashboard listener PID does not match .pids/dashboard.pid"; \
	    exit 1; \
	  fi; \
	}
	@echo ""
	@echo "  Ingestor:  http://localhost:$(_IPORT)"
	@echo "  Dashboard: http://localhost:$(_DPORT)"
	@echo "  Logs:      logs/ingestor.log   logs/dashboard.log"
	@echo "  PIDs:      .pids/ingestor.pid  .pids/dashboard.pid"

# clean-start: STRICT PRODUCTION path.
# Requires .env with a real HELIUS_API_KEY (not the placeholder).
# Verifies clustering backend is healthy after start.
# For development without a live key use: make clean-start-debug
clean-start: clean-stop
	@echo "=== clean-start: pre-flight checks ==="
	@[ -f .env ] || { echo "FAIL: .env does not exist. Run: make setup-live-env"; exit 1; }
	@KEY=$$(grep "^HELIUS_API_KEY=" .env 2>/dev/null | head -1 | cut -d'=' -f2-); \
	scripts/validate-helius-key.sh "$$KEY"
	@$(MAKE) _start_services INGESTOR_EXTRA_ENV=""
	@echo "=== clean-start: verifying clustering backend ==="
	@HEALTH=$$(curl -sf --max-time 5 http://localhost:$(_IPORT)/healthz) || \
		{ echo "FAIL: ingestor /healthz unreachable"; $(MAKE) clean-stop; exit 1; }; \
	BACKEND=$$(echo "$$HEALTH" | python3 -c "import sys,json; print(json.load(sys.stdin).get('clustering',{}).get('backend','null'))" 2>/dev/null); \
	HEALTHY=$$(echo "$$HEALTH" | python3 -c "import sys,json; print(json.load(sys.stdin).get('clustering',{}).get('healthy',False))" 2>/dev/null); \
	echo "  backend=$$BACKEND healthy=$$HEALTHY"; \
	{ [ "$$HEALTHY" = "True" ] || { echo "FAIL: backend=$$BACKEND not healthy. Check HELIUS_API_KEY validity."; $(MAKE) clean-stop; exit 1; }; }; \
	echo "PASS: clustering backend=$$BACKEND healthy=true — strict production mode active"

# clean-start-debug: development path — no HELIUS_API_KEY required.
# Forces NullResolver (HELIUS_API_KEY and FUNDER_MAP_PATH cleared).
# BUY/READY are blocked by CLUSTER_REQUIRED=1.
clean-start-debug: clean-stop
	@echo "[DEBUG] Starting with NullResolver — BUY/READY will be disabled"
	@$(MAKE) _start_services INGESTOR_EXTRA_ENV="HELIUS_API_KEY= FUNDER_MAP_PATH="

# status: truthful report — pidfile, live pid, port, and healthz.
# Never claims a service is healthy unless its port is bound AND /healthz returns 200.
status:
	@echo "=== Ingestor (port $(_IPORT)) ==="
	@{ \
	  if [ -f .pids/ingestor.pid ]; then \
	    pid=$$(cat .pids/ingestor.pid); \
	    if kill -0 "$$pid" 2>/dev/null; then \
	      echo "  pid     : $$pid  (alive)"; \
	    else \
	      echo "  pid     : $$pid  (DEAD — stale pidfile)"; \
	    fi; \
	  else \
	    echo "  pid     : (no pidfile)"; \
	  fi; \
	  listener=$$(lsof -iTCP:$(_IPORT) -sTCP:LISTEN -n -P 2>/dev/null | awk 'NR>1{print $$2}' | head -1); \
	  if [ -n "$$listener" ]; then \
	    echo "  port    : $(_IPORT) LISTENING (pid $$listener)"; \
	  else \
	    echo "  port    : $(_IPORT) NOT listening"; \
	  fi; \
	  if curl -sf --max-time 2 http://localhost:$(_IPORT)/healthz > /dev/null 2>&1; then \
	    echo "  healthz : OK  (http://localhost:$(_IPORT)/healthz)"; \
	  else \
	    echo "  healthz : UNREACHABLE"; \
	  fi; \
	}
	@echo ""
	@echo "=== Dashboard (port $(_DPORT)) ==="
	@{ \
	  if [ -f .pids/dashboard.pid ]; then \
	    pid=$$(cat .pids/dashboard.pid); \
	    if kill -0 "$$pid" 2>/dev/null; then \
	      echo "  pid     : $$pid  (alive)"; \
	    else \
	      echo "  pid     : $$pid  (DEAD — stale pidfile)"; \
	    fi; \
	  else \
	    echo "  pid     : (no pidfile)"; \
	  fi; \
	  listener=$$(lsof -iTCP:$(_DPORT) -sTCP:LISTEN -n -P 2>/dev/null | awk 'NR>1{print $$2}' | head -1); \
	  if [ -n "$$listener" ]; then \
	    echo "  port    : $(_DPORT) LISTENING (pid $$listener)"; \
	  else \
	    echo "  port    : $(_DPORT) NOT listening"; \
	  fi; \
	  if curl -sf --max-time 2 http://localhost:$(_DPORT)/healthz > /dev/null 2>&1; then \
	    echo "  healthz : OK  (http://localhost:$(_DPORT)/healthz)"; \
	  else \
	    echo "  healthz : UNREACHABLE"; \
	  fi; \
	}

# tail-ingestor: stream ingestor logs.
tail-ingestor:
	@tail -f logs/ingestor.log

# tail-dashboard: stream dashboard logs.
tail-dashboard:
	@tail -f logs/dashboard.log

# reset-state: clear the live in-memory store via the admin endpoint.
# Requires the running ingestor to have been started with ENABLE_LOCAL_ADMIN=1.
# Only reachable from localhost.
reset-state:
	@echo "Sending reset to ingestor (requires ENABLE_LOCAL_ADMIN=1 on the running ingestor)..."
	@curl -sf -X POST \
		"http://localhost:$(_IPORT)/admin/reset-state?confirm=RESET_LIVE_STATE" \
		| python3 -m json.tool || \
		echo "ERROR: reset failed — is ENABLE_LOCAL_ADMIN=1 set on the running ingestor?"
