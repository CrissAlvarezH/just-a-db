.PHONY: start down

setup:
	sh scripts/entrypoint.sh setup

destroy:
	sh scripts/entrypoint.sh destroy

status:
	sh scripts/entrypoint.sh status

connect:
	sh scripts/entrypoint.sh connect

user-data:
	sh scripts/entrypoint.sh user-data
