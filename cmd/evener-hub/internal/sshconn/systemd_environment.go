package sshconn

import "primeradiant.com/evener/execsupport/shellquote"

// Read at most 64 KiB and return only the four identity inputs, never unrelated
// credentials. This is an EnvironmentFile parser, not shell evaluation: dollar
// signs are literal, quotes only delimit a value when they begin it, and an
// unquoted backslash escapes the next byte. Multiline quoted values refuse.
const systemdEnvironmentFileFilter = `use strict;
use Encode qw(decode FB_CROAK LEAVE_SRC);
my ($path, $optional) = @ARGV;
open my $f, '<', $path or do { exit 0 if $optional eq 'yes' && !-e $path; exit 1 };
-f $f or exit 1;
my $text = ''; my $n = read($f, $text, 65537);
defined($n) && $n <= 65536 && !($text =~ /\x00/) or exit 1;
close($f) or exit 1;
my $decoded = eval { decode('UTF-8', $text, FB_CROAK | LEAVE_SRC) };
defined($decoded) or exit 1;
for (split //, $decoded) {
 my $c = ord($_);
 exit 1 if $c == 0xfeff || ($c >= 0xfdd0 && $c <= 0xfdef) || ($c & 0xffff) >= 0xfffe;
}
my %env;
my @lines = split /\n/, $text, -1;
while (@lines) {
 my $line = shift @lines;
 next if $line =~ /^\s*(?:[#;]|$)/;
 next unless $line =~ /^\s*([A-Za-z_][A-Za-z_0-9]*)\s*=(.*)$/s;
 my ($key, $raw) = ($1, $2);
 $raw =~ s/^[ \t\r]+//;
 while ($raw !~ /^'/ && $raw =~ /(\\+)$/ && length($1) % 2) {
  @lines or exit 1;
  chop $raw; $raw .= shift @lines;
 }
 my $value = '';
 if ($raw =~ /^'/) {
  $raw =~ /^'([^']*)'[ \t\r]*$/s or exit 1; $value = $1;
 } elsif ($raw =~ /^"/) {
  $raw =~ /^"((?:[^"\\]|\\.)*)"[ \t\r]*$/s or exit 1; $value = $1;
  $value =~ s/\\([\$\x60"\\])/$1/g;
 } else {
  my $white = '';
  while (length $raw) {
   my $c = substr($raw, 0, 1, '');
   if ($c eq "\\") {
    length($raw) or exit 1;
    $value .= $white . substr($raw, 0, 1, ''); $white = '';
   } elsif ($c =~ /[ \t\r]/) { $white .= $c }
   else { $value .= $white . $c; $white = '' }
  }
 }
 if ($key =~ /^(HOME|XDG_CONFIG_HOME|XDG_STATE_HOME|EVENER_HUB_LISTEN_ADDR)$/) {
  length($value) <= 16384 && $value !~ /[\r\n\x00]/ or exit 1;
  $env{$key} = $value;
 }
}
print "$_=$env{$_}\0" for sort keys %env;
`

func systemdEnvironmentFileRemote(path string, optional bool) string {
	mode := "no"
	if optional {
		mode = "yes"
	}
	return "perl -e " + shellquote.RemoteWord(systemdEnvironmentFileFilter) + " -- " + shellquote.RemoteWord(path) + " " + mode
}
