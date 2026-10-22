package sshconn

import (
	"strings"
	"testing"
)

// Declare the extra ordered, read-only gate observations explicitly at each
// mutation site. ownerRunner remains unchanged and rejects every unknown call.
func replacementGateCommands(t *testing.T, inspection []ownerCommand, live bool) []ownerCommand {
	t.Helper()
	var commands []ownerCommand
	if live {
		commands = append(commands, inspection...)
	}
	var contextReport, listing, userListing, definition, show, cwd ownerCommand
	var darwin bool
	for _, command := range inspection {
		switch {
		case strings.HasPrefix(command.command, "CDPATH=") && strings.Contains(command.command, " hub endpoint "):
			contextReport = command
		case strings.HasPrefix(command.command, "CDPATH=") && strings.HasSuffix(command.command, " && pwd -P"):
			cwd = command
		case command.command == "launchctl list":
			listing, darwin = command, true
		case strings.HasPrefix(command.command, "launchctl print "):
			show = command
		case command.command == systemctlListUnits:
			listing = command
		case command.command == systemctlListUnitsUser:
			userListing = command
		case strings.HasPrefix(command.command, "definition=$("):
			definition = command
		}
	}
	if contextReport.command == "" {
		t.Fatal("missing declared owner-context report")
	}
	if !live {
		if cwd.command == "" {
			t.Fatal("missing declared physical working directory")
		}
		commands = append(commands, cwd)
	}
	if darwin {
		commands = append(commands, listing, show, contextReport)
		return commands
	}
	if definition.command != "" {
		var label string
		for _, command := range inspection {
			if !strings.HasPrefix(command.command, "systemctl show --property=Id,MainPID") {
				continue
			}
			_, after, _ := strings.Cut(command.command, " -- ")
			if after != "" && strings.Contains(definition.command, " -- "+after+")") {
				show, label = command, after
			}
		}
		if label == "" {
			t.Fatal("missing declared selected service")
		}
		commands = append(commands, listing, userListing, show, definition,
			ownerCommand{"systemctl show --property=User,EnvironmentFiles,PassEnvironment,UnsetEnvironment,SetLoginEnvironment -- " + label, "User=dev\nEnvironmentFiles=\nPassEnvironment=\nUnsetEnvironment=\nSetLoginEnvironment=yes\n"},
			ownerCommand{"getent passwd dev | cut -d: -f6", "/home/dev\n"},
			ownerCommand{"values=$(systemctl show --property=Environment --value -- " + label + ") || exit; printf '%s\\n' \"$values\" | " + narrowEnvironmentFilter, ""})
	}
	commands = append(commands, contextReport)
	return commands
}
