import os
import subprocess
import boto3
from psycopg2 import connect

PG_HOST = os.getenv("DB_HOST")
PG_PORT = os.getenv("DB_PORT")
PG_USER = os.getenv("DB_USER")
PG_PASSWORD = os.getenv("DB_PASSWORD")

S3_BUCKET_NAME = os.getenv("S3_BUCKET")
S3_BACKUP_FOLDER = "backups"


def list_databases():
    try:
        conn = connect(
            host=PG_HOST,
            port=PG_PORT,
            user=PG_USER,
            password=PG_PASSWORD,
            dbname="postgres"
        )
        cur = conn.cursor()
        cur.execute("SELECT datname FROM pg_database WHERE datistemplate = false;")
        databases = [row[0] for row in cur.fetchall()]
        cur.close()
        conn.close()
        return databases
    except Exception as e:
        print(f"Error listing databases: {e}")
        return []


def backup_database(db_name, backup_dir):
    backup_file = os.path.join(backup_dir, f"{db_name}.sql")
    try:
        subprocess.run(
            [
                "pg_dump",
                "-h", PG_HOST,
                "-p", PG_PORT,
                "-U", PG_USER,
                "-F", "c",  # Custom format
                "-f", backup_file,
                db_name,
            ],
            check=True,
            env={"PGPASSWORD": PG_PASSWORD},
        )
        return backup_file
    except subprocess.CalledProcessError as e:
        print(f"Error backing up database {db_name}: {e}")
        return None


def upload_to_s3(file_path, bucket_name, s3_folder):
    s3_key = os.path.join(s3_folder, os.path.basename(file_path))
    try:
        s3 = boto3.client("s3")
        s3.upload_file(file_path, bucket_name, s3_key)
        print(f"Uploaded {file_path} to s3://{bucket_name}/{s3_key}")
    except Exception as e:
        print(f"Error uploading {file_path} to S3: {e}")


def main():
    backup_dir = "./backups"
    os.makedirs(backup_dir, exist_ok=True)

    databases = list_databases()
    print(f"Databases: {databases}")
    if not databases:
        print("No databases found or error occurred.")
        return

    for db in databases:
        print(f"Backing up database: '{db}'")
        backup_file = backup_database(db, backup_dir)
        if backup_file:
            print(f"Uploading backup for db '{db}' to S3: '{backup_file}'")
            upload_to_s3(backup_file, S3_BUCKET_NAME, S3_BACKUP_FOLDER)
            os.remove(backup_file)

    print("Backup process completed.")


if __name__ == "__main__":
    main()