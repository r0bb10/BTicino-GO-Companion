#!/usr/bin/env sh
set -eu

SERVICE_NAME="companion"
BASE_DIR="/home/bticino/cfg/extra/companion"
BIN_PATH="${BASE_DIR}/companion"
INIT_SCRIPT="/etc/init.d/${SERVICE_NAME}"

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
LOCAL_INIT_TEMPLATE="${SCRIPT_DIR}/init.d/companion"

DEFAULT_RELEASE_REPO="r0bb10/BTicino-GO-Companion"
REPO="${COMPANION_RELEASE_REPO:-${DEFAULT_RELEASE_REPO}}"
RELEASE_TAG="${COMPANION_RELEASE_TAG:-}"
BUNDLE_ASSET="${COMPANION_RELEASE_BUNDLE_ASSET:-companion.tar.gz}"
HEALTHCHECK_TIMEOUT_SEC="${COMPANION_HEALTHCHECK_TIMEOUT_SEC:-45}"

if [ -n "${RELEASE_TAG}" ]; then
	DEFAULT_BASE_URL="https://github.com/${REPO}/releases/download/${RELEASE_TAG}"
	DEFAULT_RELEASE_API="https://api.github.com/repos/${REPO}/releases/tags/${RELEASE_TAG}"
else
	DEFAULT_BASE_URL="https://github.com/${REPO}/releases/latest/download"
	DEFAULT_RELEASE_API="https://api.github.com/repos/${REPO}/releases/latest"
fi

BASE_URL="${COMPANION_RELEASE_BASE_URL:-${DEFAULT_BASE_URL}}"
RELEASE_API="${COMPANION_RELEASE_API:-${DEFAULT_RELEASE_API}}"

FLEXISIP_USERS_DB="/etc/flexisip/users/users.db.txt"
FLEXISIP_ROUTE="/etc/flexisip/users/route.conf"
FLEXISIP_ROUTE_INT="/etc/flexisip/users/route_int.conf"
FLEXISIP_ROUTE_EXT="/etc/flexisip/users/route_ext.conf"
FLEXISIP_DOMAIN_REG="/etc/flexisip/domain-registration.conf"
SIP_USER="companion"
SIP_TARGET="sip:127.0.0.1:5070;transport=tcp"

ROOT=""
ROOT_WAS_REMOUNTED=0
FAILURES=0
POST_CHECK_FAILURES=0
SELECTED_BINARY_PATH=""
SELECTED_INIT_TEMPLATE=""
SELECTED_GST_DIR=""
FLEXISIP_TEMP_FILES=""

log() {
	printf 'INFO: %s\n' "$*"
}

ok() {
	printf 'OK: %s\n' "$*"
}

warn() {
	printf 'WARN: %s\n' "$*"
}

fail() {
	printf 'FAIL: %s\n' "$*"
	FAILURES=$((FAILURES + 1))
}

cleanup_download_dir() {
	if [ -n "${ROOT}" ] && [ -d "${ROOT}" ]; then
		rm -rf "${ROOT}" || true
	fi
}

cleanup_flexisip_temp_files() {
	if [ -n "${FLEXISIP_TEMP_FILES}" ]; then
		# These files are rendered before any Flexisip configuration is replaced.
		rm -f ${FLEXISIP_TEMP_FILES} || true
		FLEXISIP_TEMP_FILES=""
	fi
}

restore_root_ro() {
	if [ "${ROOT_WAS_REMOUNTED}" -eq 1 ]; then
		mount -o remount,ro / || true
		ROOT_WAS_REMOUNTED=0
		log "Restored / as read-only."
	fi
}

on_exit() {
	cleanup_flexisip_temp_files
	cleanup_download_dir
	restore_root_ro
}

trap on_exit 0 HUP INT TERM

require_root() {
	if [ "$(id -u)" -ne 0 ]; then
		log "This script must run as root."
		exit 1
	fi
}

remount_root_rw() {
	if [ "${ROOT_WAS_REMOUNTED}" -eq 0 ]; then
		mount -o remount,rw /
		ROOT_WAS_REMOUNTED=1
		log "Remounted / as read-write."
	fi
}

fetch() {
	url="$1"
	destination="$2"

	if command -v wget >/dev/null 2>&1; then
		wget -qO "${destination}" "${url}"
		return
	fi

	if command -v curl >/dev/null 2>&1; then
		curl -fsSL -o "${destination}" "${url}"
		return
	fi

	log "Neither wget nor curl is available."
	exit 1
}

sha256_file() {
	target="$1"

	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "${target}" | awk '{print $1}'
		return
	fi

	if command -v busybox >/dev/null 2>&1; then
		busybox sha256sum "${target}" | awk '{print $1}'
		return
	fi

	if command -v openssl >/dev/null 2>&1; then
		openssl dgst -sha256 "${target}" | awk '{print $NF}'
		return
	fi

	log "No SHA256 tool found (sha256sum/busybox/openssl)."
	exit 1
}

parse_release_asset_digest() {
	awk -v asset="$2" '
		BEGIN { want = 0 }
		$0 ~ /"name"[[:space:]]*:/ && index($0, "\"" asset "\"") > 0 { want = 1 }
		want && $0 ~ /"digest"[[:space:]]*:/ {
			line = $0
			sub(/.*"digest"[[:space:]]*:[[:space:]]*"sha256:/, "", line)
			sub(/".*/, "", line)
			if (line != "") {
				print tolower(line)
				exit
			}
		}
	' "$1"
}

resolve_bundle_sha() {
	if [ -n "${COMPANION_RELEASE_BUNDLE_SHA256:-}" ]; then
		printf '%s' "${COMPANION_RELEASE_BUNDLE_SHA256}" | tr 'A-F' 'a-f'
		return
	fi

	release_json="${ROOT}/release.json"
	fetch "${RELEASE_API}" "${release_json}" 2>/dev/null || return 1
	parse_release_asset_digest "${release_json}" "${BUNDLE_ASSET}"
}

companion_firewall_ports() {
	printf '%s\n' "8080 80 8554 51826"
}

companion_firewall_udp_ports() {
	printf '%s\n' "5353 8555"
}

ensure_persistent_firewall_port_value() {
	hook="/etc/network/if-pre-up.d/iptables"
	port="$1"

	if [ ! -f "${hook}" ]; then
		warn "${hook} not found, skipping persistent firewall patch."
		return
	fi

	if awk -v port="${port}" '
		/# ssh \& sip enabled/ { inblock=1; next }
		inblock && /^[[:space:]]*for i in .*; do[[:space:]]*$/ {
			n=split($0,a,/[^0-9]+/)
			for(i=1;i<=n;i++) if(a[i]==port) found=1
			inblock=0
		}
		END { exit(found ? 0 : 1) }
	' "${hook}"; then
		log "Persistent firewall already allows companion port ${port}."
		return
	fi

	tmp="${hook}.tmp.$$"
	if awk -v port="${port}" '
		BEGIN { patched=0; inblock=0 }
		/# ssh \& sip enabled/ { inblock=1; print; next }
		inblock && /^[[:space:]]*for i in .*; do[[:space:]]*$/ {
			line=$0
			sub(/^[[:space:]]*for i in[[:space:]]*/, "", line)
			sub(/[[:space:]]*; do[[:space:]]*$/, "", line)
			n=split(line,a,/[[:space:]]+/)
			out="for i in"
			has=0
			for(i=1;i<=n;i++) if(a[i]!="") {
				if(a[i]==port) has=1
				out=out " " a[i]
			}
			if(!has) out=out " " port
			print out "; do"
			patched=1
			inblock=0
			next
		}
		{ print }
		END { if(!patched) exit 42 }
	' "${hook}" > "${tmp}"; then
		cp "${tmp}" "${hook}"
		rm -f "${tmp}"
		log "Persisted companion firewall port ${port} in ${hook}."
		return
	fi

	rc=$?
	rm -f "${tmp}"
	if [ "${rc}" -eq 42 ]; then
		warn "could not find SSH/SIP firewall block in ${hook}; no persistent patch applied."
	else
		warn "failed to patch ${hook} for companion port ${port}."
	fi
}

ensure_persistent_firewall_udp_port_value() {
	hook="/etc/network/if-pre-up.d/iptables"
	port="$1"

	if [ ! -f "${hook}" ]; then
		warn "${hook} not found, skipping persistent firewall patch."
		return
	fi

	if grep -Eq "udp.*--dport[[:space:]]+${port}.*-j[[:space:]]+ACCEPT" "${hook}"; then
		log "Persistent firewall already allows UDP ${port}."
		return
	fi

	tmp="${hook}.tmp.$$"
	if awk -v port="${port}" '
		BEGIN { patched=0 }
		/^#disable all other stuff/ && !patched {
			print "# companion udp service"
			print "iptables -A INPUT -p udp -m udp --dport " port " -j ACCEPT"
			print ""
			patched=1
		}
		{ print }
		END { if(!patched) exit 42 }
	' "${hook}" > "${tmp}"; then
		cp "${tmp}" "${hook}"
		rm -f "${tmp}"
		log "Persisted companion UDP firewall port ${port} in ${hook}."
		return
	fi

	rc=$?
	rm -f "${tmp}"
	if [ "${rc}" -eq 42 ]; then
		warn "could not find firewall policy marker in ${hook}; no UDP persistent patch applied."
	else
		warn "failed to patch ${hook} for companion UDP port ${port}."
	fi
}

ensure_persistent_firewall_ports() {
	for port in $(companion_firewall_ports); do
		ensure_persistent_firewall_port_value "${port}"
	done

	for port in $(companion_firewall_udp_ports); do
		ensure_persistent_firewall_udp_port_value "${port}"
	done
}

install_binary() {
	mkdir -p "${BASE_DIR}"
	candidate="${BASE_DIR}/companion.candidate.$$"
	cp "$1" "${candidate}"
	chmod 755 "${candidate}"

	if [ -f "${BIN_PATH}" ]; then
		cp -f "${BIN_PATH}" "${BASE_DIR}/companion.previous"
		chmod 755 "${BASE_DIR}/companion.previous" || true
	fi

	mv -f "${candidate}" "${BIN_PATH}"
	log "Installed binary to ${BIN_PATH}"
}

install_gst_runtime() {
	source_dir="$1"

	if [ -z "${source_dir}" ] || [ ! -d "${source_dir}" ]; then
		log "No bundled gst runtime provided; keeping existing runtime."
		return
	fi

	mkdir -p "${BASE_DIR}"
	candidate="${BASE_DIR}/gst.candidate.$$"
	previous="${BASE_DIR}/gst.previous"
	rm -rf "${candidate}"
	mkdir -p "${candidate}"
	cp -a "${source_dir}/." "${candidate}/"
	rm -rf "${previous}"
	if [ -d "${BASE_DIR}/gst" ]; then
		mv -f "${BASE_DIR}/gst" "${previous}"
	fi
	mv -f "${candidate}" "${BASE_DIR}/gst"
	log "Installed gst runtime to ${BASE_DIR}/gst"
}

install_init_script() {
	cp -f "$1" "${INIT_SCRIPT}"
	chmod 755 "${INIT_SCRIPT}"
}

register_service() {
	remount_root_rw
	install_init_script "$1"

	for runlevel in 2 3 4 5; do
		dir="/etc/rc${runlevel}.d"
		link="${dir}/S99zz${SERVICE_NAME}"
		if [ -d "${dir}" ]; then
			rm -f "${link}"
			ln -s "../init.d/${SERVICE_NAME}" "${link}"
		fi
	done

	for runlevel in 0 1 6; do
		dir="/etc/rc${runlevel}.d"
		link="${dir}/K55${SERVICE_NAME}"
		if [ -d "${dir}" ]; then
			rm -f "${link}"
			ln -s "../init.d/${SERVICE_NAME}" "${link}"
		fi
	done

	ensure_persistent_firewall_ports
	log "Registered init service ${SERVICE_NAME}"
}

start_service() {
	[ ! -x "${INIT_SCRIPT}" ] || "${INIT_SCRIPT}" restart || "${INIT_SCRIPT}" start
}

health_url() {
	printf '%s\n' "http://127.0.0.1:8080/api/v3/health"
}

health_endpoint_reachable() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsS --max-time 3 "$1" >/dev/null 2>&1
		return
	fi

	if command -v wget >/dev/null 2>&1; then
		wget -q -T 3 -O /dev/null "$1" >/dev/null 2>&1
		return
	fi

	return 127
}

wait_for_health() {
	elapsed=0
	while [ "${elapsed}" -lt "$2" ]; do
		if health_endpoint_reachable "$1"; then
			return
		fi
		sleep 1
		elapsed=$((elapsed + 1))
	done
	return 1
}

flexisip_domain() {
	domain=""
	if [ -r "${FLEXISIP_DOMAIN_REG}" ]; then
		domain="$(awk '!/^[[:space:]]*#/ && NF { print $1; exit }' "${FLEXISIP_DOMAIN_REG}" || true)"
		if [ -n "${domain}" ]; then
			printf '%s\n' "${domain}"
			return
		fi
	fi

	for file in /etc/flexisip/flexisip.conf /home/bticino/cfg/flexisip.conf; do
		[ -r "${file}" ] || continue
		domain="$(awk -F= '/^[[:space:]]*(aliases|reg-domains|auth-domains)=/ { print $2; exit }' "${file}" | awk '{ print $1; exit }' || true)"
		if [ -n "${domain}" ]; then
			printf '%s\n' "${domain}"
			return
		fi
	done

	return 1
}

backup_once() {
	[ -f "$1" ] || return 1
	[ -f "$1.companion.bak" ] || cp -p "$1" "$1.companion.bak"
}

replace_if_changed() {
	file="$1"
	tmp="$2"
	if cmp -s "${file}" "${tmp}"; then
		rm -f "${tmp}"
		return
	fi
	mv -f "${tmp}" "${file}"
}

render_user_db() {
	file="$1"
	tmp="$2"
	user="$3"
	domain="$4"

	if awk -v user="${user}" '$1 == user { found = 1 } END { exit(found ? 0 : 1) }' "${file}"; then
		cp -p "${file}" "${tmp}"
		return
	fi

	hash="$(awk -v suffix="@${domain}" 'length($1) > length(suffix) && substr($1, length($1) - length(suffix) + 1) == suffix && $2 != "" { print $2; exit }' "${file}" || true)"
	[ -n "${hash}" ] || return 1
	cp -p "${file}" "${tmp}"
	printf '%s %s ;\n' "${user}" "${hash}" >> "${tmp}"
}

render_alluser_route() {
	file="$1"
	tmp="$2"
	domain="$3"
	member="$4"

	awk -v alluser="<sip:alluser@${domain}>" -v member="<sip:${member}>" '
		$1 == alluser {
			count++
			if (index($0, member) == 0) print $0 ", " member
			else print
			next
		}
		{ print }
		END { exit(count == 1 ? 0 : 1) }
	' "${file}" > "${tmp}"
}

render_active_routes() {
	file="$1"
	tmp="$2"
	domain="$3"
	member="$4"
	target="$5"

	awk -v alluser="<sip:alluser@${domain}>" -v member="<sip:${member}>" -v target="<${target}>" '
		$1 == alluser {
			alluser_count++
			if (index($0, member) == 0) print $0 ", " member
			else print
			next
		}
		$1 == member {
			direct_count++
			if (direct_count == 1) print member " " target
			next
		}
		{ print }
		END {
			if (alluser_count != 1) exit 1
			if (direct_count == 0) print member " " target
		}
	' "${file}" > "${tmp}"
}

render_direct_route() {
	file="$1"
	tmp="$2"
	member="$3"
	target="$4"

	awk -v member="<sip:${member}>" -v target="<${target}>" '
		$1 == member {
			count++
			if (count == 1) print member " " target
			next
		}
		{ print }
		END { if (count == 0) print member " " target }
	' "${file}" > "${tmp}"
}

provision_flexisip() {
	domain="$(flexisip_domain || true)"
	if [ -z "${domain}" ]; then
		warn "Could not determine the Flexisip domain; skipping SIP provisioning."
		return
	fi

	user="${SIP_USER}@${domain}"
	for file in "${FLEXISIP_USERS_DB}" "${FLEXISIP_ROUTE}" "${FLEXISIP_ROUTE_INT}" "${FLEXISIP_ROUTE_EXT}"; do
		if [ ! -f "${file}" ]; then
			warn "Missing ${file}; skipping SIP provisioning."
			return
		fi
	done

	tmp_user="${FLEXISIP_USERS_DB}.companion.$$"
	tmp_route="${FLEXISIP_ROUTE}.companion.$$"
	tmp_int="${FLEXISIP_ROUTE_INT}.companion.$$"
	tmp_ext="${FLEXISIP_ROUTE_EXT}.companion.$$"
	FLEXISIP_TEMP_FILES="${tmp_user} ${tmp_route} ${tmp_int} ${tmp_ext}"

	if ! render_user_db "${FLEXISIP_USERS_DB}" "${tmp_user}" "${user}" "${domain}" ||
		! render_active_routes "${FLEXISIP_ROUTE}" "${tmp_route}" "${domain}" "${user}" "${SIP_TARGET}" ||
		! render_alluser_route "${FLEXISIP_ROUTE_INT}" "${tmp_int}" "${domain}" "${user}" ||
		! render_direct_route "${FLEXISIP_ROUTE_EXT}" "${tmp_ext}" "${user}" "${SIP_TARGET}"; then
		cleanup_flexisip_temp_files
		warn "Could not build complete Flexisip provisioning changes."
		return
	fi

	for file in "${FLEXISIP_USERS_DB}" "${FLEXISIP_ROUTE}" "${FLEXISIP_ROUTE_INT}" "${FLEXISIP_ROUTE_EXT}"; do
		backup_once "${file}" || return 1
	done

	replace_if_changed "${FLEXISIP_USERS_DB}" "${tmp_user}"
	replace_if_changed "${FLEXISIP_ROUTE}" "${tmp_route}"
	replace_if_changed "${FLEXISIP_ROUTE_INT}" "${tmp_int}"
	replace_if_changed "${FLEXISIP_ROUTE_EXT}" "${tmp_ext}"
	FLEXISIP_TEMP_FILES=""
	log "Flexisip provisioning verified for ${user}."
}

post_install_checks() {
	FAILURES=0
	pidfile="/var/run/${SERVICE_NAME}.pid"

	if [ -x "${BIN_PATH}" ]; then
		ok "Binary exists: ${BIN_PATH}"
	else
		fail "Binary missing or not executable: ${BIN_PATH}"
	fi

	if [ -x "${INIT_SCRIPT}" ]; then
		ok "Init script present: ${INIT_SCRIPT}"
	else
		fail "Init script missing: ${INIT_SCRIPT}"
	fi

	if [ -L "/etc/rc5.d/S99zz${SERVICE_NAME}" ]; then
		ok "Boot symlink present: /etc/rc5.d/S99zz${SERVICE_NAME}"
	else
		fail "Boot symlink missing: /etc/rc5.d/S99zz${SERVICE_NAME}"
	fi

	if [ -x "${INIT_SCRIPT}" ] && "${INIT_SCRIPT}" status >/dev/null 2>&1; then
		ok "Service is running"
	elif [ -f "${pidfile}" ] && pid="$(cat "${pidfile}" 2>/dev/null || true)" && [ -n "${pid}" ] && [ -d "/proc/${pid}" ]; then
		ok "Service process exists via pidfile ${pidfile} (pid ${pid})"
	else
		fail "Service not running"
	fi

	if [ -x "${BASE_DIR}/gst/bin/gst-launch-1.0" ] || [ -x "${BASE_DIR}/gst/opt/gst14/bin/gst-launch-1.0" ]; then
		ok "GStreamer launcher is present"
	else
		fail "GStreamer launcher missing in ${BASE_DIR}/gst"
	fi

	url="$(health_url)"
	if command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1; then
		log "Waiting for health endpoint (up to ${HEALTHCHECK_TIMEOUT_SEC}s): ${url}"
		if wait_for_health "${url}" "${HEALTHCHECK_TIMEOUT_SEC}"; then
			ok "Health endpoint reachable at ${url}"
		else
			fail "Health endpoint not reachable at ${url} after ${HEALTHCHECK_TIMEOUT_SEC}s"
		fi
	else
		fail "Neither curl nor wget available for health check"
	fi

	if awk '$2=="/"{print $4}' /proc/mounts | grep -Eq '(^|,)ro(,|$)'; then
		ok "Root filesystem is read-only"
	else
		fail "Root filesystem is not read-only"
	fi

	POST_CHECK_FAILURES="${FAILURES}"
	if [ "${FAILURES}" -eq 0 ]; then
		log "Post-install checks passed."
	else
		log "Post-install checks completed with ${FAILURES} failure(s)."
	fi
}

resolve_local_install_inputs() {
	if [ -z "$1" ] || [ ! -f "$1" ]; then
		log "Missing companion binary for install."
		exit 1
	fi

	if [ ! -f "$2" ]; then
		log "Missing init template for install."
		exit 1
	fi

	SELECTED_BINARY_PATH="$1"
	SELECTED_INIT_TEMPLATE="$2"
	if [ -d "${SCRIPT_DIR}/../gst" ]; then
		SELECTED_GST_DIR="${SCRIPT_DIR}/../gst"
	fi
}

download_latest_artifacts() {
	if [ -z "${REPO}" ]; then
		log "Set COMPANION_RELEASE_REPO to download a release."
		exit 1
	fi

	ROOT="/tmp/companion-install.$$"
	mkdir -p "${ROOT}"
	log "Downloading latest release bundle..."
	bundle="${ROOT}/${BUNDLE_ASSET}"
	fetch "${BASE_URL}/${BUNDLE_ASSET}" "${bundle}"

	expected_sha="$(resolve_bundle_sha)"
	if [ -z "${expected_sha}" ]; then
		log "Could not resolve expected SHA256 digest for ${BUNDLE_ASSET}."
		exit 1
	fi

	actual_sha="$(sha256_file "${bundle}")"
	if [ "${actual_sha}" != "${expected_sha}" ]; then
		log "SHA256 mismatch for ${BUNDLE_ASSET}. Expected: ${expected_sha}; Actual: ${actual_sha}"
		exit 1
	fi

	tar -xzf "${bundle}" -C "${ROOT}"
	bundle_dir="${ROOT}/companion"
	if [ ! -f "${bundle_dir}/companion" ] || [ ! -f "${bundle_dir}/init.d/companion" ] || [ ! -d "${bundle_dir}/gst" ]; then
		log "Bundle asset ${BUNDLE_ASSET} not found or incomplete in latest release."
		exit 1
	fi

	chmod 755 "${bundle_dir}/companion" "${bundle_dir}/init.d/companion"
	SELECTED_BINARY_PATH="${bundle_dir}/companion"
	SELECTED_INIT_TEMPLATE="${bundle_dir}/init.d/companion"
	SELECTED_GST_DIR="${bundle_dir}/gst"
}

main() {
	require_root
	log "Starting companion installation"

	if [ -n "${1:-}" ]; then
		log "Using local binary input: $1"
		resolve_local_install_inputs "$1" "${LOCAL_INIT_TEMPLATE}"
	else
		download_latest_artifacts
	fi

	install_binary "${SELECTED_BINARY_PATH}"
	install_gst_runtime "${SELECTED_GST_DIR}"
	register_service "${SELECTED_INIT_TEMPLATE}"
	start_service
	provision_flexisip
	restore_root_ro
	post_install_checks

	if [ "${POST_CHECK_FAILURES}" -ne 0 ]; then
		log "Installation finished with ${POST_CHECK_FAILURES} failed check(s)."
		exit 1
	fi

	log "Installation complete."
}

main "$@"
