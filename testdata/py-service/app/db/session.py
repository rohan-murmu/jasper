from sqlalchemy import create_engine

from app.core.config import DATABASE_URL

engine = create_engine(DATABASE_URL)


def get_session():
    return engine.connect()
