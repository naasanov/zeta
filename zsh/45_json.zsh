
#--------------------------------------------------------------------#
# Minimal JSON helpers                                               #
#--------------------------------------------------------------------#
# The wire protocol (daemon/internal/protocol) is newline-delimited JSON. zsh
# has no JSON tooling, and forking jq on every keystroke is exactly the
# fork-per-request cost the daemon exists to avoid (design §48). These two
# pure-zsh helpers cover what the client needs: escape a string into a JSON
# request, and pull a flat string field out of a one-line JSON reply.
#
# Scope/limits (documented, adequate for shell command lines):
#  - Encoding escapes " \ and the \n \t \r control chars. Other control bytes
#    (< 0x20) are passed through; command buffers don't contain them.
#  - Decoding understands the \" \\ \/ \n \t \r escapes. It does NOT decode
#    \uXXXX — which is why the daemon MUST encode with HTML escaping disabled so
#    shell metacharacters (< > &) stay literal (see the protocol package doc).

# Escape $1 as a JSON string body (no surrounding quotes) into $REPLY.
# Backslash is replaced first so the escapes we introduce aren't re-escaped.
_zsh_autopilot_json_escape() {
  emulate -L zsh
  local s=$1
  s=${s//'\'/'\\'}
  s=${s//'"'/'\"'}
  s=${s//$'\n'/'\n'}
  s=${s//$'\t'/'\t'}
  s=${s//$'\r'/'\r'}
  REPLY=$s
}

# Extract the string value of flat key $2 from one-line JSON object $1 into
# $REPLY. Returns non-zero if the key is absent. The regex tolerates escaped
# quotes inside the value ((\\.|[^"\\])*), then the escapes are undone. A
# sentinel byte (0x01) protects literal "\\" so a following n/t/r isn't misread
# as a control escape.
_zsh_autopilot_json_str_field() {
  emulate -L zsh
  local json=$1 key=$2
  local -a match mbegin mend
  local re
  re='"'${key}'":"((\\.|[^"\\])*)"'

  [[ $json =~ $re ]] || { REPLY=; return 1 }

  local raw=$match[1]
  raw=${raw//'\\'/$'\x01'}
  raw=${raw//'\n'/$'\n'}
  raw=${raw//'\t'/$'\t'}
  raw=${raw//'\r'/$'\r'}
  raw=${raw//'\"'/'"'}
  raw=${raw//'\/'/'/'}
  raw=${raw//$'\x01'/'\'}
  REPLY=$raw
  return 0
}
