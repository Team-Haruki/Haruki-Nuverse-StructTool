use std::collections::{HashMap, HashSet};
use std::io::Cursor;
use std::sync::Arc;

use serde_json::{Map as JsonMap, Value as JsonValue};

#[derive(Debug, Clone)]
pub struct Schema {
    pub kind: SchemaKind,
    pub name: Option<String>,
    pub fields: Vec<Field>,
    pub items: Option<Arc<Schema>>,
    pub values: Option<Arc<Schema>>,
    pub key_type: Option<String>,
    pub union_of: Vec<Arc<Schema>>,
    pub union_disp: Vec<UnionVariant>,
}

#[derive(Debug, Clone)]
pub enum SchemaKind {
    Null,
    Boolean,
    Int,
    Long,
    Float,
    Double,
    Bytes,
    String,
    Record,
    Array,
    Map,
    Union,
    Ref,
}

#[derive(Debug, Clone)]
pub struct Field {
    pub name: String,
    pub ty: Arc<Schema>,
    pub msgpack_key: MsgpackKey,
}

#[derive(Debug, Clone)]
pub enum MsgpackKey {
    String(String),
    Int(i64),
}

#[derive(Debug, Clone)]
pub struct UnionVariant {
    pub key: i64,
    pub ty: String,
}

pub type Registry = HashMap<String, Arc<Schema>>;

pub fn load_file(path: &str) -> Result<(Registry, Arc<Schema>), String> {
    let data = std::fs::read_to_string(path).map_err(|e| e.to_string())?;
    load_bytes(data.as_bytes())
}

pub fn load_bytes(data: &[u8]) -> Result<(Registry, Arc<Schema>), String> {
    let raw: JsonValue = serde_json::from_slice(data).map_err(|e| e.to_string())?;
    let mut builder = SchemaBuilder::default();
    let root = builder.parse_root(&raw)?;
    Ok((builder.registry, root))
}

pub fn decode(schema: &Arc<Schema>, data: &[u8], registry: &Registry) -> Result<JsonValue, String> {
    let mut cursor = Cursor::new(data);
    let value = rmpv::decode::read_value(&mut cursor).map_err(|e| e.to_string())?;
    let json = rmpv_to_json(value)?;
    restore_json(schema, &json, registry)
}

pub fn encode(
    _schema: &Arc<Schema>,
    _value: &JsonValue,
    _registry: &Registry,
) -> Result<Vec<u8>, String> {
    Err("encode is not implemented in the Rust library yet".to_string())
}

#[derive(Default)]
struct SchemaBuilder {
    registry: Registry,
}

impl SchemaBuilder {
    fn parse_root(&mut self, raw: &JsonValue) -> Result<Arc<Schema>, String> {
        if let Some(arr) = raw.as_array() {
            let mut first = None;
            for item in arr {
                let parsed = self.parse_schema(item)?;
                if first.is_none() {
                    first = Some(parsed);
                }
            }
            first.ok_or_else(|| "empty schema array".to_string())
        } else {
            self.parse_schema(raw)
        }
    }

    fn parse_schema(&mut self, raw: &JsonValue) -> Result<Arc<Schema>, String> {
        match raw {
            JsonValue::String(name) => Ok(self.primitive_or_ref(name)),
            JsonValue::Array(items) => {
                let union_of = items
                    .iter()
                    .map(|item| self.parse_schema(item))
                    .collect::<Result<Vec<_>, _>>()?;
                Ok(Arc::new(Schema {
                    kind: SchemaKind::Union,
                    name: None,
                    fields: Vec::new(),
                    items: None,
                    values: None,
                    key_type: None,
                    union_of,
                    union_disp: Vec::new(),
                }))
            }
            JsonValue::Object(obj) => self.parse_object(obj),
            _ => Ok(Arc::new(Schema {
                kind: SchemaKind::Null,
                name: None,
                fields: Vec::new(),
                items: None,
                values: None,
                key_type: None,
                union_of: Vec::new(),
                union_disp: Vec::new(),
            })),
        }
    }

    fn parse_object(
        &mut self,
        obj: &serde_json::Map<String, JsonValue>,
    ) -> Result<Arc<Schema>, String> {
        let ty = obj
            .get("type")
            .ok_or_else(|| "schema missing type".to_string())?;
        if ty.is_array() {
            return self.parse_schema(ty);
        }
        match ty.as_str().unwrap_or("null") {
            "record" => self.parse_record(obj),
            "array" => Ok(Arc::new(Schema {
                kind: SchemaKind::Array,
                name: None,
                fields: Vec::new(),
                items: Some(
                    self.parse_schema(
                        obj.get("items")
                            .ok_or_else(|| "array missing items".to_string())?,
                    )?,
                ),
                values: None,
                key_type: None,
                union_of: Vec::new(),
                union_disp: Vec::new(),
            })),
            "map" => Ok(Arc::new(Schema {
                kind: SchemaKind::Map,
                name: None,
                fields: Vec::new(),
                items: None,
                values: Some(
                    self.parse_schema(
                        obj.get("values")
                            .ok_or_else(|| "map missing values".to_string())?,
                    )?,
                ),
                key_type: obj
                    .get("msgpack_key_type")
                    .and_then(|v| v.as_str())
                    .map(str::to_string),
                union_of: Vec::new(),
                union_disp: Vec::new(),
            })),
            primitive => Ok(self.primitive_or_ref(primitive)),
        }
    }

    fn parse_record(
        &mut self,
        obj: &serde_json::Map<String, JsonValue>,
    ) -> Result<Arc<Schema>, String> {
        let short_name = obj
            .get("name")
            .and_then(|v| v.as_str())
            .unwrap_or_default()
            .to_string();
        let namespace = obj
            .get("namespace")
            .and_then(|v| v.as_str())
            .unwrap_or_default();
        let full_name = if !namespace.is_empty() && !short_name.contains('.') {
            format!("{namespace}.{short_name}")
        } else {
            short_name.clone()
        };

        let fields = obj
            .get("fields")
            .and_then(|v| v.as_array())
            .map(|items| {
                items
                    .iter()
                    .map(|item| self.parse_field(item))
                    .collect::<Result<Vec<_>, _>>()
            })
            .transpose()?
            .unwrap_or_default();

        let union_disp = obj
            .get("msgpack_unions")
            .and_then(|v| v.as_array())
            .map(|items| {
                items
                    .iter()
                    .filter_map(|item| {
                        Some(UnionVariant {
                            key: item.get("key")?.as_i64()?,
                            ty: item.get("type")?.as_str()?.to_string(),
                        })
                    })
                    .collect()
            })
            .unwrap_or_default();

        let schema = Arc::new(Schema {
            kind: SchemaKind::Record,
            name: Some(full_name.clone()),
            fields,
            items: None,
            values: None,
            key_type: None,
            union_of: Vec::new(),
            union_disp,
        });

        if !full_name.is_empty() {
            self.registry.insert(full_name, schema.clone());
        }
        if !short_name.is_empty() {
            self.registry.insert(short_name, schema.clone());
        }
        Ok(schema)
    }

    fn parse_field(&mut self, raw: &JsonValue) -> Result<Field, String> {
        let obj = raw
            .as_object()
            .ok_or_else(|| "field must be object".to_string())?;
        let name = obj
            .get("name")
            .and_then(|v| v.as_str())
            .ok_or_else(|| "field missing name".to_string())?
            .to_string();
        let ty = self.parse_schema(
            obj.get("type")
                .ok_or_else(|| "field missing type".to_string())?,
        )?;
        let msgpack_key = match obj.get("msgpack_key") {
            Some(JsonValue::Number(n)) => MsgpackKey::Int(n.as_i64().unwrap_or_default()),
            Some(JsonValue::String(s)) => MsgpackKey::String(s.clone()),
            _ => MsgpackKey::String(name.clone()),
        };
        Ok(Field {
            name,
            ty,
            msgpack_key,
        })
    }

    fn primitive_or_ref(&self, name: &str) -> Arc<Schema> {
        let kind = match name {
            "null" => SchemaKind::Null,
            "boolean" => SchemaKind::Boolean,
            "int" => SchemaKind::Int,
            "long" => SchemaKind::Long,
            "float" => SchemaKind::Float,
            "double" => SchemaKind::Double,
            "bytes" => SchemaKind::Bytes,
            "string" => SchemaKind::String,
            _ => SchemaKind::Ref,
        };
        Arc::new(Schema {
            kind,
            name: Some(name.to_string()),
            fields: Vec::new(),
            items: None,
            values: None,
            key_type: None,
            union_of: Vec::new(),
            union_disp: Vec::new(),
        })
    }
}

fn resolve_schema(schema: &Arc<Schema>, registry: &Registry) -> Arc<Schema> {
    if matches!(schema.kind, SchemaKind::Ref) {
        if let Some(name) = &schema.name {
            if let Some(real) = registry.get(name) {
                return real.clone();
            }
        }
    }
    schema.clone()
}

fn restore_json(
    schema: &Arc<Schema>,
    value: &JsonValue,
    registry: &Registry,
) -> Result<JsonValue, String> {
    let schema = resolve_schema(schema, registry);
    match schema.kind {
        SchemaKind::Null
        | SchemaKind::Boolean
        | SchemaKind::Int
        | SchemaKind::Long
        | SchemaKind::Float
        | SchemaKind::Double
        | SchemaKind::Bytes
        | SchemaKind::String
        | SchemaKind::Ref => Ok(value.clone()),
        SchemaKind::Union => {
            if value.is_null() {
                return Ok(JsonValue::Null);
            }
            let next = schema
                .union_of
                .iter()
                .find(|item| !matches!(resolve_schema(item, registry).kind, SchemaKind::Null))
                .cloned();
            match next {
                Some(item) => restore_json(&item, value, registry),
                None => Ok(value.clone()),
            }
        }
        SchemaKind::Array => {
            let items = schema
                .items
                .as_ref()
                .ok_or_else(|| "array missing items".to_string())?;
            let arr = value
                .as_array()
                .ok_or_else(|| "value is not array".to_string())?;
            arr.iter()
                .map(|item| restore_json(items, item, registry))
                .collect::<Result<Vec<_>, _>>()
                .map(JsonValue::Array)
        }
        SchemaKind::Map => {
            let values = schema
                .values
                .as_ref()
                .ok_or_else(|| "map missing values".to_string())?;
            let obj = value
                .as_object()
                .ok_or_else(|| "value is not object".to_string())?;
            let mut out = JsonMap::new();
            for (k, v) in obj {
                out.insert(k.clone(), restore_json(values, v, registry)?);
            }
            Ok(JsonValue::Object(out))
        }
        SchemaKind::Record => restore_record(&schema, value, registry),
    }
}

fn restore_record(
    schema: &Schema,
    value: &JsonValue,
    registry: &Registry,
) -> Result<JsonValue, String> {
    if !schema.union_disp.is_empty() {
        let arr = value
            .as_array()
            .ok_or_else(|| "union dispatch is not array".to_string())?;
        if arr.len() != 2 {
            return Ok(value.clone());
        }
        let discriminator = arr[0].as_i64().unwrap_or_default();
        let payload_schema = schema
            .union_disp
            .iter()
            .find(|variant| variant.key == discriminator)
            .and_then(|variant| registry.get(&variant.ty));
        let payload = match payload_schema {
            Some(schema) => restore_json(schema, &arr[1], registry)?,
            None => arr[1].clone(),
        };
        return Ok(serde_json::json!({"__type": discriminator, "value": payload}));
    }

    if let Some(arr) = value.as_array() {
        let mut out = JsonMap::new();
        for field in &schema.fields {
            if let MsgpackKey::Int(idx) = field.msgpack_key {
                if let Some(item) = arr.get(idx as usize) {
                    out.insert(field.name.clone(), restore_json(&field.ty, item, registry)?);
                }
            }
        }
        return Ok(JsonValue::Object(out));
    }

    if let Some(obj) = value.as_object() {
        let mut out = JsonMap::new();
        let mut consumed = HashSet::new();
        for field in &schema.fields {
            let key = match &field.msgpack_key {
                MsgpackKey::String(key) => key.clone(),
                MsgpackKey::Int(idx) => idx.to_string(),
            };
            let raw = obj.get(&key);
            if let Some(item) = raw {
                consumed.insert(key);
                out.insert(field.name.clone(), restore_json(&field.ty, item, registry)?);
            }
        }
        for (key, item) in obj {
            if !consumed.contains(key) {
                out.insert(key.clone(), item.clone());
            }
        }
        return Ok(JsonValue::Object(out));
    }

    Ok(value.clone())
}

fn rmpv_to_json(value: rmpv::Value) -> Result<JsonValue, String> {
    Ok(match value {
        rmpv::Value::Nil => JsonValue::Null,
        rmpv::Value::Boolean(b) => JsonValue::Bool(b),
        rmpv::Value::Integer(i) => {
            if let Some(n) = i.as_i64() {
                JsonValue::Number(n.into())
            } else if let Some(n) = i.as_u64() {
                JsonValue::Number(n.into())
            } else {
                JsonValue::Null
            }
        }
        rmpv::Value::F32(f) => serde_json::Number::from_f64(f as f64)
            .map(JsonValue::Number)
            .unwrap_or(JsonValue::Null),
        rmpv::Value::F64(f) => serde_json::Number::from_f64(f)
            .map(JsonValue::Number)
            .unwrap_or(JsonValue::Null),
        rmpv::Value::String(s) => JsonValue::String(
            s.as_str()
                .map(str::to_string)
                .unwrap_or_else(|| s.to_string()),
        ),
        rmpv::Value::Binary(_) | rmpv::Value::Ext(_, _) => JsonValue::Null,
        rmpv::Value::Array(arr) => JsonValue::Array(
            arr.into_iter()
                .map(rmpv_to_json)
                .collect::<Result<Vec<_>, _>>()?,
        ),
        rmpv::Value::Map(map) => {
            let mut out = JsonMap::new();
            for (k, v) in map {
                let key = match k {
                    rmpv::Value::String(s) => s
                        .as_str()
                        .map(str::to_string)
                        .unwrap_or_else(|| s.to_string()),
                    rmpv::Value::Integer(i) => i.to_string(),
                    _ => continue,
                };
                out.insert(key, rmpv_to_json(v)?);
            }
            JsonValue::Object(out)
        }
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn string_keyed_decode_normalizes_mapped_keys_and_preserves_unknown() {
        let schema_json = br#"{
          "type": "record",
          "name": "Summary",
          "namespace": "Test",
          "fields": [
            {"name": "id", "type": "int", "msgpack_key": "Id"},
            {"name": "exchangeCategory", "type": "string", "msgpack_key": "ExchangeCategory"}
          ]
        }"#;
        let (registry, schema) = load_bytes(schema_json).unwrap();

        let payload = rmpv::Value::Map(vec![
            (
                rmpv::Value::String("Id".into()),
                rmpv::Value::Integer(1.into()),
            ),
            (
                rmpv::Value::String("ExchangeCategory".into()),
                rmpv::Value::String("normal".into()),
            ),
            (
                rmpv::Value::String("unknownPascal".into()),
                rmpv::Value::Boolean(true),
            ),
        ]);
        let mut bytes = Vec::new();
        rmpv::encode::write_value(&mut bytes, &payload).unwrap();

        let restored = decode(&schema, &bytes, &registry).unwrap();
        assert_eq!(restored["id"], serde_json::json!(1));
        assert_eq!(restored["exchangeCategory"], serde_json::json!("normal"));
        assert!(restored.get("Id").is_none());
        assert!(restored.get("ExchangeCategory").is_none());
        assert_eq!(restored["unknownPascal"], serde_json::json!(true));
    }
}
