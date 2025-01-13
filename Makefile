.PHONY: setup download-env destroy status connect clear

setup:
	sh scripts/entrypoint.sh setup

credentials:
	sh scripts/entrypoint.sh credentials

destroy:
	sh scripts/entrypoint.sh destroy

status:
	sh scripts/entrypoint.sh status

connect:
	sh scripts/entrypoint.sh connect

user-data:
	sh scripts/entrypoint.sh user-data

clear:
	@rm -rf credentials.txt user_data_output*.log
