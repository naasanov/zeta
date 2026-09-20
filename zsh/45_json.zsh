
#--------------------------------------------------------------------#
# Minimal JSON helpers                                               #
#--------------------------------------------------------------------#

# Pure-zsh JSON: forking jq on every keystroke defeats the daemon's purpose.
# Decoding does not handle \uXXXX, so the daemon must encode with HTML
# escaping off so shell metacharacters (< > &) stay literal.

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

# Extracts the string value of flat key $2 from one-line JSON $1 into $REPLY;
# returns non-zero if absent. A sentinel byte (0x01) protects literal "\\" so
# a following n/t/r isn't misread as a control escape.
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
