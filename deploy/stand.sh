#!/usr/bin/env bash
# Load stand for cubic-root on two hosts:
#
#   app      the service in Docker on the host network with no resource limits,
#            plus Vector that ships its logs to loadgen;
#   loadgen  k6 and the rest of the observability stack from compose.yaml.
#
# Deploy from a machine with SSH access to the app host. loadgen is reached
# through the app host by its internal IP: its public IP forwards nothing.
# The app host's address stays out of git, put it into deploy/stand.env:
#   APP_SSH=user@<public IP of the app host>
# Both hosts need passwordless sudo. Docker, make and k6 are installed on first run.
#
# On your machine:
#   deploy/stand.sh up                   ship the repo and start everything (safe to rerun)
#   deploy/stand.sh app                  rebuild and restart only the app; app settings
#                                        come from the environment, for example
#       FAULT_LATENCY=50ms FAULT_LATENCY_RATE=0.2 deploy/stand.sh app
#   deploy/stand.sh tunnel               Grafana and the Victoria* UIs on localhost
#   deploy/stand.sh status               containers and scrape targets
#
# On loadgen, where `up` installs this script as `stand` (from your machine the
# same commands work as deploy/stand.sh run ... and so on):
#   stand run marathon 24h               repeat a k6 profile for 24h in the background;
#   stand run soak 12h RATE=3000         extra args go to make test-load
#   stand run marathon 48h RATE=2000 RATE_STEP=500 FAST=1
#                                        look for the limit: RATE grows by 500 every
#                                        passing cycle, then the last passing RATE holds
#   stand ps                             what is running and how far it got
#   stand logs                           follow the log of the current run
#   stand stop                           stop the run, k6 still prints its summary
set -euo pipefail

# deploy/stand.env is ignored by git and sourced only on your machine. It can set
# any variable below and wins over the environment.
STAND_ENV=$(dirname "${BASH_SOURCE[0]}")/stand.env
if [ -f "$STAND_ENV" ]; then
	set -a
	# shellcheck source=/dev/null
	. "$STAND_ENV"
	set +a
fi
APP_SSH=${APP_SSH:-}
APP_IP=${APP_IP:-192.168.213.14}
LOADGEN_SSH=${LOADGEN_SSH:-user@192.168.213.15}
LOADGEN_IP=${LOADGEN_IP:-192.168.213.15}
# Set LOADGEN_JUMP= to connect to loadgen directly.
LOADGEN_JUMP=${LOADGEN_JUMP-$APP_SSH}

# App settings from compose.yaml. Every `up` and `app` writes them anew, so a fault
# lasts until the next run without it. Per-request debug logs and 100% of traces
# take CPU from the app and gigabytes of disk per hour of load: the stand lowers both.
APP_SETTINGS="DEBUG TRACES_SAMPLE_RATIO CACHE_SIZE CACHE_TTL CALC_DELAY CALC_DELAY_JITTER FAULT_ERROR_RATE FAULT_LATENCY FAULT_LATENCY_RATE"
export DEBUG=${DEBUG:-false} TRACES_SAMPLE_RATIO=${TRACES_SAMPLE_RATIO:-0.1}

# Ports of the stack on loadgen that `tunnel` forwards: Grafana, VictoriaMetrics,
# vmagent, vmalert, VictoriaLogs, VictoriaTraces. Local ports are shifted by
# TUNNEL_OFFSET so the tunnel doesn't clash with the local compose stack.
UI_PORTS="3333 8428 8429 8880 9428 10428"
TUNNEL_OFFSET=${TUNNEL_OFFSET:-10000}

# systemd unit of the background load run on loadgen and the directory of its logs.
RUN_UNIT=stand-load
RUNS=~/stand/runs

REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TTY=""

# --- Your machine --------------------------------------------------------------

need_app_ssh() {
	if [ -z "$APP_SSH" ]; then
		echo "APP_SSH is not set: put APP_SSH=user@<public IP of the app host> into $STAND_ENV" >&2
		exit 1
	fi
}

# Remote commands are built locally on purpose.
# shellcheck disable=SC2029
ssh_app() {
	need_app_ssh
	ssh ${TTY:+-t} "$APP_SSH" "$@"
}
# shellcheck disable=SC2029
ssh_loadgen() {
	need_app_ssh
	ssh ${TTY:+-t} ${LOADGEN_JUMP:+-J "$LOADGEN_JUMP"} "$LOADGEN_SSH" "$@"
}

# ship <ssh_app|ssh_loadgen>: tracked files of the working tree, committed or not,
# go to ~/cubic-root and this script to ~/stand.
ship() {
	local rev
	rev=$(git -C "$REPO" stash create)
	git -C "$REPO" archive --format=tar.gz "${rev:-HEAD}" | "$1" '
		set -e
		rm -rf ~/.stand-src && mkdir -p ~/.stand-src ~/cubic-root ~/stand
		tar -xzf - -C ~/.stand-src
		# rsync keeps directories in place: Grafana and vmalert bind-mount them.
		rsync -a --delete --checksum ~/.stand-src/ ~/cubic-root/
		rm -rf ~/.stand-src'
	# A new file instead of rewriting in place: a running load loop is still
	# reading the old one, and bash reads scripts as it goes.
	"$1" 'cat > ~/stand/stand.sh.new && chmod +x ~/stand/stand.sh.new && mv -f ~/stand/stand.sh.new ~/stand/stand.sh' <"${BASH_SOURCE[0]}"
}

# run_on <ssh_app|ssh_loadgen> <app|loadgen>: runs the shipped script on the host.
run_on() {
	local env="APP_IP=$APP_IP LOADGEN_IP=$LOADGEN_IP" name value
	for name in $APP_SETTINGS; do
		if value=$(printenv "$name"); then env="$env $name=$(printf '%q' "$value")"; fi
	done
	"$1" "env $env bash ~/stand/stand.sh _$2"
}

up() {
	# A redeploy can restart the app and the storages in the middle of a run.
	if [ -z "${FORCE:-}" ] && ssh_loadgen "systemctl is-active --quiet $RUN_UNIT"; then
		echo "a load run is going on loadgen (deploy/stand.sh ps): finish it with deploy/stand.sh stop" >&2
		echo "or redeploy anyway with FORCE=1 deploy/stand.sh up" >&2
		exit 1
	fi
	ship ssh_loadgen
	run_on ssh_loadgen loadgen
	ship ssh_app
	run_on ssh_app app
	echo
	echo "cubic-root: http://$APP_IP:8080 from the internal network"
	echo "Load: ssh -J $LOADGEN_JUMP $LOADGEN_SSH, then stand run marathon 24h"
	echo "Grafana and the rest: deploy/stand.sh tunnel"
}

tunnel() {
	local forwards="" port o=$TUNNEL_OFFSET
	need_app_ssh
	for port in $UI_PORTS; do forwards="$forwards -L $((port + o)):$LOADGEN_IP:$port"; done
	echo "Grafana         http://localhost:$((3333 + o))  admin / $(ssh_loadgen 'cat ~/stand/grafana-password')"
	echo "VictoriaMetrics http://localhost:$((8428 + o))/vmui"
	echo "vmagent         http://localhost:$((8429 + o))/targets"
	echo "vmalert         http://localhost:$((8880 + o))"
	echo "VictoriaLogs    http://localhost:$((9428 + o))/select/vmui"
	echo "VictoriaTraces  http://localhost:$((10428 + o))/select/vmui"
	echo "Ctrl-C closes the tunnel."
	# shellcheck disable=SC2086 # one word per -L option
	exec ssh -N $forwards "$APP_SSH"
}

status() {
	need_app_ssh
	echo "== loadgen"
	ssh_loadgen 'sudo docker ps --format "table {{.Names}}\t{{.Status}}"; echo; curl -fsS http://localhost:8429/targets; df -h / | tail -1; echo; stand ps'
	echo
	echo "== app"
	ssh_app 'sudo docker ps --format "table {{.Names}}\t{{.Status}}"; curl -fsS -o /dev/null -w "GET /health %{http_code}\n" http://localhost:8080/health'
}

# --- Hosts: setup, runs from ~/stand/stand.sh ------------------------------------

log() { printf '==> %s: %s\n' "$(hostname)" "$*" >&2; }

# compose <app|loadgen> <args>: the repo's compose.yaml plus the host's override.
compose() {
	local role=$1
	shift
	sudo docker compose -p cubic-root -f ~/cubic-root/compose.yaml -f ~/stand/"$role".yaml \
		--env-file ~/stand/"$role".env "$@"
}

ensure_docker() {
	command -v docker >/dev/null && return
	log "installing Docker"
	{ curl -fsSL https://get.docker.com | sudo sh; } >~/stand/docker-install.log 2>&1 ||
		{ tail -20 ~/stand/docker-install.log; exit 1; }
}

setup_loadgen() {
	ensure_docker
	if ! command -v make >/dev/null; then
		log "installing make"
		sudo apt-get update -qq
		sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq make >/dev/null
	fi
	if ! command -v k6 >/dev/null; then
		local v
		v=$(curl -fsSL https://api.github.com/repos/grafana/k6/releases/latest | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')
		log "installing k6 $v"
		curl -fsSL "https://github.com/grafana/k6/releases/download/$v/k6-$v-linux-amd64.tar.gz" |
			sudo tar -xz -C /usr/local/bin --strip-components=1 "k6-$v-linux-amd64/k6"
	fi
	sudo ln -sfn ~/stand/stand.sh /usr/local/bin/stand
	echo "http://$APP_IP:8080" >~/stand/base-url

	# Generated once, so the password survives redeploys.
	[ -s ~/stand/grafana-password ] || openssl rand -hex 12 >~/stand/grafana-password
	echo "GRAFANA_ADMIN_PASSWORD=$(cat ~/stand/grafana-password)" >~/stand/loadgen.env

	# The app and its Vector live on the other host.
	sed -e "s/\"cubic-root:9090\"/\"$APP_IP:9090\"/" -e "s/\"vector:9598\"/\"$APP_IP:9598\"/" \
		~/cubic-root/observability/vmagent/scrape.yml >~/stand/scrape.yml
	if [ "$(grep -c "$APP_IP" ~/stand/scrape.yml)" != 2 ]; then
		log "targets in observability/vmagent/scrape.yml changed, fix the sed in deploy/stand.sh"
		exit 1
	fi

	cat >~/stand/loadgen.yaml <<EOF
services:
  vmagent:
    volumes:
      - $HOME/stand/scrape.yml:/etc/vmagent/scrape.yml:ro
  # A week of data, so a 48h run and its analysis fit whole. Data lives in volumes:
  # in compose.yaml it is inside the containers and a recreate wipes it.
  victoriametrics:
    command:
      - -storageDataPath=/storage
      - -retentionPeriod=7d
      - -search.latencyOffset=5s
    volumes:
      - victoriametrics:/storage
  # Older partitions of logs and traces go once they outgrow their caps. If the
  # disk runs low anyway, both switch to read-only long before VictoriaMetrics has to.
  victorialogs:
    command:
      - -storageDataPath=/storage
      - -retentionPeriod=7d
      - -retention.maxDiskSpaceUsageBytes=20GiB
      - -storage.minFreeDiskSpaceBytes=10GiB
    volumes:
      - victorialogs:/storage
  # Traces take the most space: about 5GB a day at 3-4k req/s with 10% sampling.
  victoriatraces:
    command:
      - -storageDataPath=/storage
      - -retentionPeriod=7d
      - -search.latencyOffset=5s
      - -retention.maxDiskSpaceUsageBytes=60GiB
      - -storage.minFreeDiskSpaceBytes=10GiB
    volumes:
      - victoriatraces:/storage
volumes:
  victoriametrics:
  victorialogs:
  victoriatraces:
EOF

	log "starting the observability stack"
	compose loadgen up -d --wait --quiet-pull victoriametrics vmagent vmalert victorialogs victoriatraces grafana
	# Scrape config and rules are bind-mounted: reload them in place, keeping state.
	compose loadgen --progress quiet kill -s HUP vmagent vmalert
}

setup_app() {
	ensure_docker
	env | grep -E "^($(echo "$APP_SETTINGS" | tr ' ' '|'))=" >~/stand/app.env || true

	sed "s#http://victorialogs:9428#http://$LOADGEN_IP:9428#" \
		~/cubic-root/observability/vector/vector.yaml >~/stand/vector.yaml
	if ! grep -q "$LOADGEN_IP:9428" ~/stand/vector.yaml; then
		log "the sink in observability/vector/vector.yaml changed, fix the sed in deploy/stand.sh"
		exit 1
	fi

	cat >~/stand/app.yaml <<EOF
services:
  cubic-root:
    # Host network keeps NAT and docker-proxy out of the request path. No cpus or
    # mem_limit: the service gets the whole machine.
    network_mode: host
    ports: !reset []
    environment:
      - OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://$LOADGEN_IP:10428/insert/opentelemetry/v1/traces
    depends_on: !override
      vector:
        condition: service_healthy
    # With DEBUG=true a day of load writes tens of GB of container logs.
    logging:
      options:
        max-size: 100m
        max-file: "3"
  vector:
    network_mode: host
    volumes:
      - $HOME/stand/vector.yaml:/etc/vector/vector.yaml:ro
    depends_on: !reset []
EOF

	log "building and starting cubic-root ($(tr '\n' ' ' <~/stand/app.env))"
	compose app up -d --build --wait --quiet-pull cubic-root vector
	compose app --progress quiet kill -s HUP vector
}

# --- loadgen: background load runs -----------------------------------------------

# seconds <duration>: 24h, 90m, 1h30m, 45s; a bare number means hours.
seconds() {
	local d=$1 total=0
	if [[ $d =~ ^[0-9]+$ ]]; then d="${d}h"; fi
	while [[ $d =~ ^([0-9]+)([hms])(.*)$ ]]; do
		case ${BASH_REMATCH[2]} in
		h) total=$((total + BASH_REMATCH[1] * 3600)) ;;
		m) total=$((total + BASH_REMATCH[1] * 60)) ;;
		s) total=$((total + BASH_REMATCH[1])) ;;
		esac
		d=${BASH_REMATCH[3]}
	done
	if [ -n "$d" ] || [ "$total" = 0 ]; then
		echo "cannot parse duration \"$1\", expected 24h, 90m or 1h30m" >&2
		return 1
	fi
	echo "$total"
}

run_start() {
	if [ $# -lt 2 ]; then
		echo "usage: stand run <profile> <duration> [VAR=value...], e.g. stand run marathon 24h" >&2
		exit 1
	fi
	local profile=$1 total=$2 testid log a rate="" step=""
	seconds "$total" >/dev/null
	for a in "${@:3}"; do
		case $a in
		RATE=*) rate=${a#RATE=} ;;
		RATE_STEP=*) step=${a#RATE_STEP=} ;;
		esac
	done
	if [ -n "$step" ] && ! [[ $rate =~ ^[1-9][0-9]*$ && $step =~ ^[1-9][0-9]*$ ]]; then
		echo "RATE_STEP needs a starting RATE, both whole req/s, e.g. RATE=2000 RATE_STEP=500" >&2
		exit 1
	fi
	if systemctl is-active --quiet "$RUN_UNIT"; then
		echo "a run is already going, see stand ps; stand stop ends it" >&2
		exit 1
	fi
	# Fails right here on an unknown profile instead of in the background.
	k6 inspect --env SCENARIO="$profile" ~/cubic-root/k6/load.js >/dev/null
	testid=$profile-$total-$(date +%Y%m%d-%H%M%S)
	mkdir -p $RUNS
	log=$RUNS/$testid.log
	: >"$log" # owned by you, not by systemd that appends to it
	ln -sfn "$log" $RUNS/current.log
	# A transient systemd unit outlives the SSH session and is easy to stop.
	sudo systemd-run --quiet --collect --unit="$RUN_UNIT" --uid="$(id -u)" --gid="$(id -g)" \
		--working-directory="$HOME/cubic-root" --setenv=HOME="$HOME" \
		--property=StandardOutput="append:$log" --property=StandardError="append:$log" \
		bash "$HOME/stand/stand.sh" _loop "$testid" "$@"
	echo "started $testid"
	echo "progress: stand ps; log: stand logs; Grafana: dashboard cubic-root · Load test, testid $testid"
}

# run_loop <testid> <profile> <duration> [VAR=value...] runs inside the systemd unit.
# Every cycle is a separate k6 run: k6 keeps metric samples for the end-of-test
# summary and thresholds, and its memory grows for as long as the run lasts. All
# cycles share the testid, so the Load test dashboard shows the run as a whole.
# The last cycle starts before the deadline and runs to its end.
#
# With RATE_STEP among the args the run looks for the limit of the environment:
# every passing cycle raises RATE by RATE_STEP, and after the first failing one
# the run holds the last passing RATE until the deadline. A cycle passes when the
# k6 summary says "Verdict: ok". A failed threshold fails it, and so does a cycle
# without a summary: k6 crashed or was OOM-killed. make turns k6's exit code into
# its own 2, so the verdict comes from the log.
run_loop() {
	local testid=$1 profile=$2 total=$3 deadline cycle=0 started rc verdict
	local log=$RUNS/$testid.log rate="" step="" good="" holding="" a args=()
	shift 3
	for a in "$@"; do
		case $a in
		RATE=*) rate=${a#RATE=} ;;
		RATE_STEP=*) step=${a#RATE_STEP=} ;;
		*) args+=("$a") ;;
		esac
	done
	deadline=$(($(date +%s) + $(seconds "$total")))
	trap 'echo "=== stopped at $(date -Is)"; exit 143' TERM INT
	echo "=== $testid: $profile against $(cat ~/stand/base-url) until $(date -d "@$deadline" -Is) $*"
	while [ "$(date +%s)" -lt "$deadline" ]; do
		cycle=$((cycle + 1))
		started=$(date +%s)
		echo "=== cycle $cycle started at $(date -Is)${rate:+, RATE=$rate}"
		rc=0
		make -s -C ~/cubic-root test-load SCENARIO="$profile" BASE_URL="$(cat ~/stand/base-url)" \
			K6_TESTID="$testid" K6_ARGS=--quiet "${args[@]}" ${rate:+RATE=$rate} || rc=$?
		verdict=$(sed -n "/^=== cycle $cycle started/,\$p" "$log" | sed -n 's/^  Verdict: //p' | tail -n 1)
		echo "=== cycle $cycle finished at $(date -Is)${rate:+, RATE=$rate}, exit code $rc: ${verdict:-no summary, k6 did not finish}"
		# A cycle that dies at once would otherwise spin until the deadline.
		if [ $(($(date +%s) - started)) -lt 10 ]; then
			echo "=== the cycle took under 10s, giving up"
			exit 1
		fi
		if [ -z "$step" ] || [ -n "$holding" ]; then
			continue
		fi
		if [[ $verdict == ok* ]]; then
			good=$rate
			rate=$((rate + step))
		elif [ -z "$good" ]; then
			echo "=== limit: the starting RATE=$rate already fails, start lower"
			exit 1
		else
			echo "=== limit: RATE=$good passed, RATE=$rate failed; holding RATE=$good until the deadline"
			rate=$good
			holding=1
		fi
	done
	if [ -n "$step" ] && [ -z "$holding" ]; then
		echo "=== limit not reached: RATE=$good passed, the deadline came first"
	fi
	echo "=== done: $cycle cycles at $(date -Is)"
}

run_ps() {
	if systemctl is-active --quiet "$RUN_UNIT"; then
		echo "running since $(systemctl show "$RUN_UNIT" -p ActiveEnterTimestamp --value)"
		# The header, the last finished cycles with their verdicts, the limit if
		# found, and the cycle going now.
		grep '^===' $RUNS/current.log | awk '
			NR == 1 { print; next }
			/^=== limit/ { limit = $0 }
			/ finished at / { done[++n] = $0 }
			{ last = $0 }
			END {
				for (i = (n > 5 ? n - 4 : 1); i <= n; i++) print done[i]
				if (limit != "") print limit
				if (last !~ / finished at |^=== limit/ && NR > 1) print last
			}'
	else
		echo "no run is going"
	fi
	if [ -d $RUNS ]; then
		echo "logs in $RUNS:"
		find $RUNS -name '*.log' -type f -printf '  %TY-%Tm-%Td %TH:%TM  %f\n' | sort -r | head -5
	fi
}

run_logs() { tail -n 40 -F $RUNS/current.log; }
run_stop() { sudo systemctl stop "$RUN_UNIT" && echo "stopped, the log keeps the k6 summary: $(readlink $RUNS/current.log)"; }

# load <run|ps|logs|stop> [args]: runs here on loadgen, or from your machine
# forwards the command there.
load() {
	if ! { command -v k6 >/dev/null && [ -f ~/stand/loadgen.env ]; }; then
		if [ -t 1 ]; then TTY=1; fi
		ssh_loadgen "stand $(printf '%q ' "$@")"
		return
	fi
	local cmd=$1
	shift
	case $cmd in
	run) run_start "$@" ;;
	ps) run_ps ;;
	logs) run_logs ;;
	stop) run_stop ;;
	esac
}

case "${1:-}" in
up) up ;;
app)
	ship ssh_app
	run_on ssh_app app
	;;
tunnel) tunnel ;;
status) status ;;
run | ps | logs | stop) load "$@" ;;
_loop)
	shift
	run_loop "$@"
	;;
_loadgen) setup_loadgen ;;
_app) setup_app ;;
*)
	sed -n '2,/^set -euo/{/^set -euo/d;s/^# \{0,1\}//;p;}' "${BASH_SOURCE[0]}"
	exit 1
	;;
esac
