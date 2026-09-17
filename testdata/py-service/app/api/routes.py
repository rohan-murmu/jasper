from app.services.billing import total
from app.api.deps import require_user


def get_total(user=require_user):
    return total()
