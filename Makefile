.PHONY: setup download-env destroy status connect clear

setup:
	sh scripts/entrypoint.sh setup

download-env:
	sh scripts/entrypoint.sh download-env

destroy:
	sh scripts/entrypoint.sh destroy

status:
	sh scripts/entrypoint.sh status

connect:
	sh scripts/entrypoint.sh connect

user-data:
	sh scripts/entrypoint.sh user-data

clear:
	@rm -rf .env.remote .db.env.remote .backup.env.remote user_data_output*.log
