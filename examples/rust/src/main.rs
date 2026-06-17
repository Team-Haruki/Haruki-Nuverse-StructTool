use std::collections::HashMap;
use std::env;
use std::fs;
use std::io::Cursor;
use std::sync::Arc;

use serde_json::{Map as JsonMap, Value as JsonValue};

#[derive(Debug, Clone)]
struct Schema {
    kind: SchemaKind,
    name: Option<String>,
    fields: Vec<Field>,
    items: Option<Arc<Schema>>,
    values: Option<Arc<Schema>>,
    union_of: Vec<Arc<Schema>>,
}

#[derive(Debug, Clone)]
enum SchemaKind {
    Null,
    Primitive,
    Record,
    Array,
    Map,
    Union,
    Ref,
}

#[derive(Debug, Clone)]
struct Field {
    name: String,
    key: MsgpackKey,
    ty: Arc<Schema>,
}

#[derive(Debug, Clone)]
enum MsgpackKey {
    String(String),
    Int(i64),
}

type Registry = HashMap<String, Arc<Schema>>;

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let mut args = env::args().skip(1);
    let mut schema_path = String::new();
    let mut class_name = String::new();
    let mut hex_data = String::new();

    while let Some(arg) = args.next() {
        match arg.as_str() {
            "--schema" => schema_path = args.next().unwrap_or_default(),
            "--class" => class_name = args.next().unwrap_or_default(),
            "--hex" => hex_data = args.next().unwrap_or_default(),
            _ => {}
        }
    }

    if schema_path.is_empty() || class_name.is_empty() || hex_data.is_empty() {
        eprintln!("usage: cargo run -- --schema <schema.avsc> --class <ClassName> --hex <hex>");
        std::process::exit(1);
    }

    let raw = fs::read_to_string(schema_path)?;
    let value: JsonValue = serde_json::from_str(&raw)?;
    let mut builder = SchemaBuilder::default();
    builder.parse_root(&value)?;
    let registry = builder.registry;
    let schema = registry
        .get(&class_name)
        .ok_or_else(|| format!("class not found: {class_name}"))?;

    let bytes = hex::decode(hex_data)?;
    let payload = read_msgpack_json(&bytes)?;
    let restored = restore_json(schema, &payload, &registry)?;
    println!("{}", serde_json::to_string_pretty(&restored)?);
    Ok(())
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
                    union_of,
                }))
            }
            JsonValue::Object(obj) => {
                let ty = obj
                    .get("type")
                    .ok_or_else(|| "schema object missing type".to_string())?;
                if ty.is_array() {
                    return self.parse_schema(ty);
                }
                match ty.as_str().unwrap_or("null") {
                    "record" => self.parse_record(obj),
                    "array" => Ok(Arc::new(Schema {
                        kind: SchemaKind::Array,
                        name: None,
                        fields: Vec::new(),
                        items: Some(self.parse_schema(obj.get("items").unwrap())?),
                        values: None,
                        union_of: Vec::new(),
                    })),
                    "map" => Ok(Arc::new(Schema {
                        kind: SchemaKind::Map,
                        name: None,
                        fields: Vec::new(),
                        items: None,
                        values: Some(self.parse_schema(obj.get("values").unwrap())?),
                        union_of: Vec::new(),
                    })),
                    primitive => Ok(self.primitive_or_ref(primitive)),
                }
            }
            _ => Ok(Arc::new(Schema {
                kind: SchemaKind::Null,
                name: None,
                fields: Vec::new(),
                items: None,
                values: None,
                union_of: Vec::new(),
            })),
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
                items.iter()
                    .map(|item| self.parse_field(item))
                    .collect::<Result<Vec<_>, _>>()
            })
            .transpose()?
            .unwrap_or_default();
        let schema = Arc::new(Schema {
            kind: SchemaKind::Record,
            name: Some(full_name.clone()),
            fields,
            items: None,
            values: None,
            union_of: Vec::new(),
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
        let ty = self.parse_schema(obj.get("type").ok_or_else(|| "field missing type".to_string())?)?;
        let key = match obj.get("msgpack_key") {
            Some(JsonValue::Number(n)) => MsgpackKey::Int(n.as_i64().unwrap_or_default()),
            Some(JsonValue::String(s)) => MsgpackKey::String(s.clone()),
            _ => MsgpackKey::String(name.clone()),
        };
        Ok(Field { name, key, ty })
    }

    fn primitive_or_ref(&self, name: &str) -> Arc<Schema> {
        let primitive = matches!(
            name,
            "null" | "boolean" | "int" | "long" | "float" | "double" | "bytes" | "string"
        );
        Arc::new(Schema {
            kind: if primitive {
                if name == "null" {
                    SchemaKind::Null
                } else {
                    SchemaKind::Primitive
                }
            } else {
                SchemaKind::Ref
            },
            name: Some(name.to_string()),
            fields: Vec::new(),
            items: None,
            values: None,
            union_of: Vec::new(),
        })
    }
}

fn restore_json(
    schema: &Arc<Schema>,
    value: &JsonValue,
    registry: &Registry,
) -> Result<JsonValue, String> {
    let schema = resolve_schema(schema, registry);
    match schema.kind {
        SchemaKind::Null | SchemaKind::Primitive | SchemaKind::Ref => Ok(value.clone()),
        SchemaKind::Union => {
            if value.is_null() {
                return Ok(JsonValue::Null);
            }
            let non_null = schema
                .union_of
                .iter()
                .find(|item| !matches!(resolve_schema(item, registry).kind, SchemaKind::Null))
                .cloned();
            match non_null {
                Some(next) => restore_json(&next, value, registry),
                None => Ok(value.clone()),
            }
        }
        SchemaKind::Array => {
            let items = schema.items.as_ref().ok_or_else(|| "array missing items".to_string())?;
            let arr = value.as_array().ok_or_else(|| "value is not array".to_string())?;
            arr.iter()
                .map(|item| restore_json(items, item, registry))
                .collect::<Result<Vec<_>, _>>()
                .map(JsonValue::Array)
        }
        SchemaKind::Map => {
            let values = schema.values.as_ref().ok_or_else(|| "map missing values".to_string())?;
            let obj = value.as_object().ok_or_else(|| "value is not object".to_string())?;
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
    if let Some(arr) = value.as_array() {
        let mut out = JsonMap::new();
        for field in &schema.fields {
            if let MsgpackKey::Int(idx) = field.key {
                if let Some(item) = arr.get(idx as usize) {
                    if !item.is_null() {
                        out.insert(field.name.clone(), restore_json(&field.ty, item, registry)?);
                    }
                }
            }
        }
        return Ok(JsonValue::Object(out));
    }
    if let Some(obj) = value.as_object() {
        let mut out = obj.clone();
        for field in &schema.fields {
            let item = match &field.key {
                MsgpackKey::String(key) => obj.get(key),
                MsgpackKey::Int(idx) => obj.get(&idx.to_string()),
            };
            if let Some(item) = item {
                if !item.is_null() {
                    out.insert(field.name.clone(), restore_json(&field.ty, item, registry)?);
                }
            }
        }
        return Ok(JsonValue::Object(out));
    }
    Ok(value.clone())
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

fn read_msgpack_json(data: &[u8]) -> Result<JsonValue, Box<dyn std::error::Error>> {
    let mut cursor = Cursor::new(data);
    let value = rmpv::decode::read_value(&mut cursor)?;
    rmpv_to_json(value)
}

fn rmpv_to_json(value: rmpv::Value) -> Result<JsonValue, Box<dyn std::error::Error>> {
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
        rmpv::Value::String(s) => JsonValue::String(s.to_string()),
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
                    rmpv::Value::String(s) => s.to_string(),
                    rmpv::Value::Integer(i) => i.to_string(),
                    _ => continue,
                };
                out.insert(key, rmpv_to_json(v)?);
            }
            JsonValue::Object(out)
        }
    })
}
