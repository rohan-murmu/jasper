use axum::Json;

use crate::domain::Invoice;
use crate::store::load;

pub struct Response;

pub async fn handler() -> Json<Invoice> {
    Json(Invoice { total: 0 })
}
