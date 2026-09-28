import { useState } from 'react';
import type { NodeConfig, NodeConfigSchema, JSONSchemaProp, DynamicEnumOption } from '../types';
import Select from './Select';

interface Props {
  schema: NodeConfigSchema;
  value: NodeConfig;
  onChange: (next: NodeConfig) => void;
  // Dynamic enum options, injected at runtime by a schema field's x_enum_source (e.g.
  // { llm_providers: ['claude', 'deepseek'] }). They take precedence over a static enum.
  dynamicEnums?: Record<string, DynamicEnumOption[]>;
}

// Which string fields get a multi-line textarea: long text and template fields such as prompt,
// system and body.
const MULTILINE_KEYS = new Set(['prompt', 'system', 'body']);

// NodeConfigForm renders a node's config form dynamically from its config_schema. The supported
// field types: enum becomes a dropdown, boolean a toggle, integer/number a number input,
// array(string) a textarea with one item per line, and string a single- or multi-line text input.
// An unknown type falls back to a JSON textarea, so any schema remains editable.
export default function NodeConfigForm({ schema, value, onChange, dynamicEnums }: Props) {
  const required = new Set(schema.required ?? []);
  const set = (key: string, v: unknown) => onChange({ ...value, [key]: v });

  // Conditional hiding: when x_hide_when matches a sibling field's current value the field is not
  // rendered — hiding the email recipients when the channel is Feishu, for instance.
  const entries = Object.entries(schema.properties ?? {}).filter(([, prop]) => {
    if (!prop.x_hide_when) return true;
    return !Object.entries(prop.x_hide_when).some(([dep, vals]) => {
      const cur = value[dep];
      return typeof cur === 'string' && vals.includes(cur);
    });
  });
  if (entries.length === 0) {
    return <p className="muted">该节点无可配置项。</p>;
  }

  return (
    <div className="node-form">
      {entries.map(([key, prop]) => (
        <Field
          key={key}
          name={key}
          prop={prop}
          required={required.has(key)}
          value={value[key]}
          onChange={(v) => set(key, v)}
          dynamicOptions={prop.x_enum_source ? dynamicEnums?.[prop.x_enum_source] : undefined}
        />
      ))}
    </div>
  );
}

function Field({
  name, prop, required, value, onChange, dynamicOptions,
}: {
  name: string;
  prop: JSONSchemaProp;
  required: boolean;
  value: unknown;
  onChange: (v: unknown) => void;
  dynamicOptions?: DynamicEnumOption[];
}) {
  const label = (
    <label>
      {prop.title || name}
      {required && <span style={{ color: 'var(--down)', marginLeft: 4 }}>*</span>}
    </label>
  );
  const hint = prop.description ? <div className="muted node-form-hint">{prop.description}</div> : null;

  // 0) A dynamic enum (x_enum_source) becomes a dropdown whose options are injected at runtime,
  //    such as the configured models or delivery channels.
  if (prop.x_enum_source) {
    const cur = typeof value === 'string' ? value : '';
    const opts = (dynamicOptions ?? []).map((o) => (typeof o === 'string' ? { value: o, label: o } : o));
    // If the current value is no longer among the options — its provider or channel having been
    // deleted or never configured — keep it as an option anyway, rather than silently dropping
    // the config.
    const optionValues = cur && !opts.some((o) => o.value === cur) ? [{ value: cur, label: cur }, ...opts] : opts;
    return (
      <div className="node-form-row">
        {label}
        {optionValues.length > 0 ? (
          <Select
            value={cur || optionValues[0].value}
            onChange={onChange}
            options={optionValues}
            style={{ width: '100%' }}
          />
        ) : (
          <div className="muted node-form-hint">暂无可选项 — 请先在「设置」中配置</div>
        )}
        {hint}
      </div>
    );
  }

  // 1) An enum becomes a dropdown.
  if (prop.enum && prop.enum.length > 0) {
    const cur = typeof value === 'string' ? value : (prop.default as string) ?? prop.enum[0];
    return (
      <div className="node-form-row">
        {label}
        <Select
          value={cur}
          onChange={onChange}
          options={prop.enum.map((e, i) => ({ value: e, label: prop.enumNames?.[i] ?? e }))}
          style={{ width: '100%' }}
        />
        {hint}
      </div>
    );
  }

  // 2) A boolean becomes a checkbox.
  if (prop.type === 'boolean') {
    const checked = typeof value === 'boolean' ? value : Boolean(prop.default);
    return (
      <div className="node-form-row">
        <label className="inline" style={{ cursor: 'pointer', textTransform: 'none', letterSpacing: 0 }}>
          <input
            type="checkbox"
            checked={checked}
            onChange={(e) => onChange(e.target.checked)}
            style={{ width: 'auto' }}
          />
          {prop.title || name}
        </label>
        {hint}
      </div>
    );
  }

  // 3) Numbers.
  if (prop.type === 'integer' || prop.type === 'number') {
    const num = typeof value === 'number' ? value : '';
    return (
      <div className="node-form-row">
        {label}
        <input
          type="number"
          value={num as number | ''}
          placeholder={prop.default !== undefined ? String(prop.default) : ''}
          step={prop.type === 'integer' ? 1 : 'any'}
          onChange={(e) => {
            const t = e.target.value;
            if (t === '') { onChange(undefined); return; }
            onChange(prop.type === 'integer' ? parseInt(t, 10) : parseFloat(t));
          }}
        />
        {hint}
      </div>
    );
  }

  // 4) A string array becomes a textarea, one item per line.
  if (prop.type === 'array') {
    return (
      <div className="node-form-row">
        {label}
        <ArrayTextarea value={value} onChange={onChange} />
        {hint}
      </div>
    );
  }

  // 5) A nested object, such as the AI analysis retention gate, renders its sub-fields
  //    recursively. An object without properties is not handled here and falls through to the
  //    JSON textarea, which is where fields of no fixed type such as gate.value end up.
  if (prop.type === 'object' && prop.properties) {
    const obj = (value && typeof value === 'object' && !Array.isArray(value))
      ? (value as Record<string, unknown>)
      : undefined;
    const enabled = obj !== undefined;
    const subEntries = Object.entries(prop.properties);
    return (
      <div className="node-form-row">
        <label className="inline" style={{ cursor: 'pointer', textTransform: 'none', letterSpacing: 0 }}>
          <input
            type="checkbox"
            checked={enabled}
            // An optional object is only submitted when checked, so an empty object cannot trip
            // the backend's required-field validation; unchecking removes it entirely.
            onChange={(e) => onChange(e.target.checked ? {} : undefined)}
            style={{ width: 'auto' }}
          />
          {prop.title || name}
        </label>
        {hint}
        {enabled && (
          <div className="node-form-nested" style={{ marginTop: 8, paddingLeft: 12, borderLeft: '2px solid var(--border)' }}>
            {subEntries.map(([subKey, subProp]) => (
              <Field
                key={subKey}
                name={subKey}
                prop={subProp}
                required={false}
                value={obj?.[subKey]}
                onChange={(v) => {
                  const next = { ...(obj ?? {}) };
                  if (v === undefined) delete next[subKey];
                  else next[subKey] = v;
                  onChange(next);
                }}
              />
            ))}
          </div>
        )}
      </div>
    );
  }

  // 6) Strings.
  if (prop.type === 'string') {
    const str = typeof value === 'string' ? value : '';
    const multiline = MULTILINE_KEYS.has(name);
    return (
      <div className="node-form-row">
        {label}
        {multiline ? (
          <textarea
            rows={name === 'prompt' ? 6 : 3}
            value={str}
            placeholder={prop.default !== undefined ? String(prop.default) : ''}
            onChange={(e) => onChange(e.target.value)}
            style={{ fontFamily: name === 'prompt' ? 'JetBrains Mono, ui-monospace, monospace' : undefined }}
          />
        ) : (
          <input
            type="text"
            value={str}
            placeholder={prop.default !== undefined ? String(prop.default) : ''}
            onChange={(e) => onChange(e.target.value)}
          />
        )}
        {hint}
      </div>
    );
  }

  // 7) The fallback: a JSON textarea.
  return (
    <div className="node-form-row">
      {label}
      <textarea
        rows={3}
        value={value === undefined ? '' : JSON.stringify(value, null, 2)}
        onChange={(e) => {
          try { onChange(JSON.parse(e.target.value)); } catch { /* leave it until the user makes it parse */ }
        }}
      />
      {hint}
    </div>
  );
}

// ArrayTextarea renders the one-item-per-line editor for a string array. It keeps the raw local
// text as the input source, which is what lets a blank line from pressing Enter survive, while
// only ever emitting the array with whitespace and blank lines stripped. It relies on
// NodeConfigForm remounting by key when the node changes to reset that local text.
function ArrayTextarea({ value, onChange }: { value: unknown; onChange: (v: unknown) => void }) {
  const initial = Array.isArray(value) ? (value as unknown[]).map((x) => String(x)).join('\n') : '';
  const [raw, setRaw] = useState(initial);
  const clean = (text: string) => text.split('\n').map((s) => s.trim()).filter((s) => s.length > 0);
  return (
    <>
      <textarea
        rows={4}
        value={raw}
        placeholder="每行一项"
        onChange={(e) => {
          setRaw(e.target.value);
          onChange(clean(e.target.value));
        }}
      />
      <div className="muted node-form-hint">每行一项，共 {clean(raw).length} 项</div>
    </>
  );
}
