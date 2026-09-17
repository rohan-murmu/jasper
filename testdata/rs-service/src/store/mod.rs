use sqlx::PgPool;

use crate::domain::Invoice;

pub async fn load(_pool: &PgPool) -> Invoice {
    Invoice { total: 0 }
}
