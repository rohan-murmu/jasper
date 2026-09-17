from app.db import get_session


def total():
    return get_session()
