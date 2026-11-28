# How the daemon reads web.listen, mirrored for evaluation time.
#
# pkg/config is the authority (WebConfig.validate and LoopbackHost): the
# daemon refuses to start with a listen address other machines can reach and
# no password. The module asserts the same rule so the mistake surfaces at
# evaluation time rather than as a failed unit, which only works if both read
# an address the same way. pkg/config/testdata/web_listen.json lists
# addresses with the daemon's verdict; the Go tests run it against the
# daemon, and this runs it against the functions below and prints the
# addresses they disagree on (an empty list when they agree):
#
#   nix eval --impure --expr 'import ./nix/modules/web-listen.nix {
#     lib = (builtins.getFlake (toString ./.)).inputs.nixpkgs.lib; check = true; }'
{
  lib,
  check ? false,
}:
let
  inherit (builtins)
    elemAt
    filter
    genList
    length
    match
    ;

  # One byte in decimal the way Go's net.ParseIP accepts it: no leading zero.
  octet = "(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])";
  ipv4 = "${octet}\\.${octet}\\.${octet}\\.${octet}";
  isGroup = text: match "[0-9a-fA-F]{1,4}" text != null;
  isZero = text: match "0{1,4}" text != null;

  # parse splits "host:port" the way net.SplitHostPort does: an IPv6 host has
  # brackets, any other host has no colon. It returns null for anything the
  # daemon calls invalid, including a port outside 0-65535.
  parse =
    listen:
    let
      port = "0*([0-9]{1,5})";
      bracketed = match "\\[(.*)]:${port}" listen;
      plain = match "([^:]*):${port}" listen;
      parts = if bracketed != null then bracketed else plain;
      host = elemAt parts 0;
      number = lib.toIntBase10 (elemAt parts 1);
    in
    if parts == null || lib.hasInfix "[" host || lib.hasInfix "]" host || number > 65535 then
      null
    else
      {
        inherit host;
        port = number;
      };

  # groups returns the eight 16-bit groups of an IPv6 literal as written,
  # with "::" filled in, or null when the text is not one. A dotted IPv4 tail
  # stays one entry that stands for the last two groups.
  groups =
    host:
    let
      halves = lib.splitString "::" host;
      side = text: if text == "" then [ ] else lib.splitString ":" text;
      compressed = length halves == 2;
      left = side (elemAt halves 0);
      right = if compressed then side (elemAt halves 1) else [ ];
      written = left ++ right;
      last = if written == [ ] then "" else lib.last written;
      tail4 = match ipv4 last != null;
      plain = if tail4 then lib.init written else written;
      count = length plain + (if tail4 then 2 else 0);
      valid =
        length halves <= 2
        && lib.all isGroup plain
        && (if compressed then count <= 7 else count == 8);
    in
    if !valid then null else left ++ genList (_: "0") (8 - count) ++ right;

  # isLoopback mirrors LoopbackHost: a host only this machine can reach. An
  # empty host means every interface.
  isLoopback =
    host:
    let
      trimmed = elemAt (match "[ \t\r\n]*(.*[^ \t\r\n]|)[ \t\r\n]*" host) 0;
      name = lib.removeSuffix "." (lib.toLower trimmed);
      six = groups name;
      lastGroup = lib.last six;
      firstFive = lib.all isZero (lib.take 5 six);
      # ::1, however its zeros are written.
      loopback6 = length six == 8 && lib.all isZero (lib.take 7 six) && match "0{0,3}1" lastGroup != null;
      # An IPv4-mapped address, ::ffff:127.0.0.1 or ::ffff:7f00:1, is the
      # IPv4 address to Go, and 127.0.0.0/8 is loopback.
      mapped4 =
        firstFive
        && lib.toLower (elemAt six 5) == "ffff"
        && (
          if length six == 7 then
            match "127\\..*" lastGroup != null
          else
            match "7[fF][0-9a-fA-F]{2}" (elemAt six 6) != null
        );
    in
    name == "localhost"
    || lib.hasSuffix ".localhost" name
    || match "127\\.${octet}\\.${octet}\\.${octet}" name != null
    || (six != null && (loopback6 || mapped4));

  cases = builtins.fromJSON (builtins.readFile ../../pkg/config/testdata/web_listen.json);
  verdict =
    listen:
    let
      parsed = parse listen;
    in
    {
      valid = parsed != null;
      loopback = parsed != null && isLoopback parsed.host;
    };
  disagreements = filter (
    case:
    let
      got = verdict case.listen;
    in
    got.valid != case.valid || got.loopback != case.loopback
  ) cases;
in
if check then
  map (case: case // { module = verdict case.listen; }) disagreements
else
  {
    inherit parse isLoopback;
  }
