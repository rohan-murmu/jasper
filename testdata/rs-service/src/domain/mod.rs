use serde::Serialize;

// A domain type reaching up into the HTTP layer: the dependency that makes the
// domain untestable without a web server.
use crate::api::Response;

#[derive(Serialize)]
pub struct Invoice {
    pub total: i64,
}

pub fn render(_r: Response) {}
