# server → Hostkey Russia

The bot runs on Russia using its shared local Telegram Bot API and volume.
STASH_URL points over the encrypted tailnet to http://100.64.0.11:8080.
NO_PROXY must include that address. Stop the source polling instance before
starting the destination. Log out the former API session when changing from
public Telegram to the local API. Never start two polling instances.

CI tests/builds once, publishes immutable GHCR digests, and deploys them on
Russia. DEPLOY_ENABLED=true opts into CD; repository VM_* secrets override the
old organization target. No bot/application build runs on the VM.
Source bot remains stopped with restart=no; configuration and original image
are retained for rollback. Stop Russia before moving the active session back.
