#!/usr/bin/env bash
# detect-cgroup.sh — detect cgroup version and output the correct
# cAdvisor Docker Compose override to use.
#
# Usage:
#   ./detect-cgroup.sh                  # prints version + recommendation
#   docker compose -f docker-compose.observability.yml \
#     -f $(./detect-cgroup.sh --compose-file) up -d
#
# cgroup v1: Ubuntu 20.04, kernel < 5.8, or if systemd.unified_cgroup_hierarchy=0
# cgroup v2: Ubuntu 22.04+, kernel >= 5.8 with unified hierarchy enabled

set -euo pipefail

CGROUP_V2_PATH="/sys/fs/cgroup/cgroup.controllers"
COMPOSE_V1="docker-compose.cadvisor-cgroupv1.yml"
COMPOSE_V2="docker-compose.cadvisor-cgroupv2.yml"

detect_cgroup_version() {
  if [ -f "$CGROUP_V2_PATH" ]; then
    echo "2"
  else
    echo "1"
  fi
}

CGROUP_VERSION=$(detect_cgroup_version)

if [ "${1:-}" = "--compose-file" ]; then
  if [ "$CGROUP_VERSION" = "2" ]; then
    echo "$COMPOSE_V2"
  else
    echo "$COMPOSE_V1"
  fi
  exit 0
fi

echo "cgroup version: $CGROUP_VERSION"

if [ "$CGROUP_VERSION" = "2" ]; then
  echo ""
  echo "✓ cgroup v2 detected — cAdvisor can run without privileged mode."
  echo "  Using: $COMPOSE_V2"
  echo "  cAdvisor uses specific capabilities instead of full root."
else
  echo ""
  echo "⚠ cgroup v1 detected — cAdvisor requires privileged: true."
  echo "  Using: $COMPOSE_V1"
  echo ""
  echo "  To migrate to cgroup v2 (requires reboot):"
  echo "  1. echo 'GRUB_CMDLINE_LINUX=\"systemd.unified_cgroup_hierarchy=1\"' >> /etc/default/grub"
  echo "  2. update-grub && reboot"
  echo "  3. Verify: ls /sys/fs/cgroup/cgroup.controllers"
  echo ""
  echo "  cgroup v2 reduces cAdvisor blast radius from full root to:"
  echo "    SYS_PTRACE  — read process stats"
  echo "    DAC_READ_SEARCH — read container filesystems"
fi
