.PHONY: start down

setup:
	sh scripts/entrypoint.sh setup

destroy:
	sh scripts/entrypoint.sh destroy

status:
	sh scripts/entrypoint.sh status

start:
	sh scripts/entrypoint.sh start

down:
	sh scripts/entrypoint.sh down
