import os

# Reaches past the db package's public API, and reads config directly.
from app.db.session import engine


def wipe():
    print(os.environ["ADMIN_TOKEN"])
    return engine
