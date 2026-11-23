#!/bin/sh
# Double-click on a Mac to set up the Palate print agent. Terminal opens,
# asks for this Mac's password, and runs the agent's guided setup.
cd "$(dirname "$0")" || exit 1
echo "Palate print agent setup"
echo "Type this Mac's password when asked (nothing shows as you type)."
echo
sudo ./palate-agent setup
echo
echo "You can close this window."
