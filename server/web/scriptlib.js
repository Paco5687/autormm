// Ready-made scripts.
//
// An empty script editor is a poor starting point for the jobs an RMM is
// actually used for, and most of them are the same handful everywhere: what is
// eating the disk, what failed to start, is anything waiting on a reboot. These
// load into the editor rather than running from here — nothing in this file
// executes until somebody saves it and picks a target, so a catalogue entry is
// a starting point to read and edit, not a button that touches the fleet.
//
// Read-only by default. The few that change something say so in `destructive`,
// which the picker shows as a warning, and none of them delete anything a
// package manager or the OS would not delete itself.
window.SCRIPT_LIBRARY = [
  // ---- Linux ----
  {
    name: 'Disk: biggest directories',
    os: 'linux', shell: 'sh',
    about: 'Top 20 space consumers under /, one level deep per filesystem.',
    content: `# Biggest directories on the root filesystem, largest first.
# -x keeps it on one filesystem so network mounts are not walked.
du -xh --max-depth=2 / 2>/dev/null | sort -rh | head -20`,
  },
  {
    name: 'Systemd: failed units',
    os: 'linux', shell: 'sh',
    about: 'Anything that failed to start, with the last lines of why.',
    content: `# What failed, then the reason for each.
systemctl --failed --no-legend --no-pager || exit 0
echo
for u in $(systemctl --failed --no-legend --no-pager | awk '{print $1}'); do
  echo "== $u"
  journalctl -u "$u" -n 15 --no-pager 2>/dev/null
  echo
done`,
  },
  {
    name: 'Reboot required?',
    os: 'linux', shell: 'sh',
    about: 'Reports whether a package update is waiting on a restart.',
    content: `# Debian/Ubuntu leave a marker; RHEL family answers via needs-restarting.
if [ -f /var/run/reboot-required ]; then
  echo "reboot required"
  cat /var/run/reboot-required.pkgs 2>/dev/null
elif command -v needs-restarting >/dev/null 2>&1; then
  needs-restarting -r || true
else
  echo "no reboot marker found"
fi`,
  },
  {
    name: 'Logs: largest files',
    os: 'linux', shell: 'sh',
    about: 'The log files actually filling /var/log, plus the journal size.',
    content: `# Largest files under /var/log, then how much the journal itself holds.
find /var/log -type f -printf '%s %p\\n' 2>/dev/null | sort -rn | head -15 |
  awk '{ printf "%8.1f MB  %s\\n", $1/1048576, $2 }'
echo
journalctl --disk-usage 2>/dev/null || true`,
  },
  {
    name: 'Docker: reclaim space',
    os: 'linux', shell: 'sh', destructive: true,
    about: 'Removes stopped containers, unused networks and dangling images.',
    content: `# Shows what is in use, then reclaims. Dangling images only: -a would
# also delete images with no running container, which on a homelab is most of
# the ones you want to keep.
docker system df || exit 1
echo
docker system prune -f`,
  },

  // ---- Windows ----
  {
    name: 'Disk: biggest folders',
    os: 'windows', shell: 'powershell',
    about: 'Top 20 folders by size on the system drive.',
    content: `# Two levels deep, largest first. SilentlyContinue because a normal
# user cannot read every profile folder and that should not stop the report.
Get-ChildItem $env:SystemDrive\\ -Directory -ErrorAction SilentlyContinue |
  ForEach-Object {
    $size = (Get-ChildItem $_.FullName -Recurse -File -ErrorAction SilentlyContinue |
      Measure-Object Length -Sum).Sum
    [pscustomobject]@{ Folder = $_.FullName; GB = [math]::Round($size / 1GB, 2) }
  } | Sort-Object GB -Descending | Select-Object -First 20 | Format-Table -AutoSize`,
  },
  {
    name: 'Services: not running but set to auto',
    os: 'windows', shell: 'powershell',
    about: 'Automatic-start services that are stopped — usually the fault.',
    content: `Get-CimInstance Win32_Service |
  Where-Object { $_.StartMode -eq 'Auto' -and $_.State -ne 'Running' } |
  Select-Object Name, DisplayName, State, StartMode | Format-Table -AutoSize`,
  },
  {
    name: 'Reboot required?',
    os: 'windows', shell: 'powershell',
    about: 'Checks the servicing and Windows Update reboot flags.',
    content: `# The two flags that actually mean a restart is owed. Deliberately not
# PendingFileRenameOperations, which is present on plenty of healthy machines
# and reports a reboot that is never needed.
$keys = @(
  'HKLM:\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Component Based Servicing\\RebootPending',
  'HKLM:\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\WindowsUpdate\\Auto Update\\RebootRequired'
)
$pending = $keys | Where-Object { Test-Path $_ }
if ($pending) { "reboot required"; $pending } else { "no reboot pending" }`,
  },
  {
    name: 'Recent crashes and errors',
    os: 'windows', shell: 'powershell',
    about: 'System and Application errors from the last 24 hours.',
    content: `Get-WinEvent -FilterHashtable @{
  LogName = 'System','Application'; Level = 1,2; StartTime = (Get-Date).AddDays(-1)
} -ErrorAction SilentlyContinue |
  Select-Object TimeCreated, LogName, ProviderName, Id, Message |
  Sort-Object TimeCreated -Descending | Select-Object -First 30 | Format-List`,
  },
  {
    name: 'Clear temp files',
    os: 'windows', shell: 'powershell', destructive: true,
    about: 'Empties the user and system temp folders. Skips files in use.',
    content: `# Reports what it reclaimed. Files held open by a running process are
# skipped rather than treated as failures.
$before = (Get-ChildItem $env:TEMP -Recurse -File -ErrorAction SilentlyContinue |
  Measure-Object Length -Sum).Sum
Get-ChildItem $env:TEMP -Recurse -ErrorAction SilentlyContinue |
  Remove-Item -Recurse -Force -ErrorAction SilentlyContinue
$after = (Get-ChildItem $env:TEMP -Recurse -File -ErrorAction SilentlyContinue |
  Measure-Object Length -Sum).Sum
"reclaimed {0:N1} MB" -f (($before - $after) / 1MB)`,
  },

  // ---- macOS ----
  {
    name: 'Disk: biggest directories',
    os: 'darwin', shell: 'sh',
    about: 'Top 20 space consumers in the home directory.',
    content: `du -xh -d 3 "$HOME" 2>/dev/null | sort -rh | head -20`,
  },
  {
    name: 'Homebrew: outdated packages',
    os: 'darwin', shell: 'sh',
    about: 'What Homebrew would update, without updating anything.',
    content: `# Reports only. Run 'brew upgrade' yourself once the list looks right.
if ! command -v brew >/dev/null 2>&1; then echo "homebrew not installed"; exit 0; fi
brew update >/dev/null 2>&1 || true
brew outdated --verbose`,
  },

  // ---- any platform ----
  {
    name: 'Who is logged in',
    os: '', shell: 'sh',
    about: 'Current sessions and recent logins. Linux and macOS.',
    content: `who
echo
last -n 15 2>/dev/null || true`,
  },
];
