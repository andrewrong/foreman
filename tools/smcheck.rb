#!/usr/bin/env ruby
# smcheck: exhaustive check of spec/design/state-machine.yaml (prototype of test:state-table-exhaustive)
require 'yaml'
sm = YAML.load_file(ARGV[0] || 'spec/design/state-machine.yaml')
d = sm['dims']
errors = []
TYPES = d['types'].keys
groups = d['type_groups']
def expand_types(t, groups, all); t == 'all' ? all : (t.is_a?(String) ? groups.fetch(t) { [t] } : t.flat_map { |x| groups.fetch(x) { [x] } }); end
def as_list(x); x.is_a?(Array) ? x : [x]; end

# ---- configurations ----
configs = []
TYPES.each do |ty|
  stages = d['types'][ty]['stages']
  stages.each { |st| configs << { state: 'queued', stage: st, type: ty } }
  rs = groups['work'].include?(ty) ? d['running_stages']['work'] : d['running_stages']['integrate']
  rs.each { |st| configs << { state: 'running', stage: st, type: ty } }
  d['waiting_reasons'].each do |r, spec|
    next unless expand_types(spec['types'], groups, TYPES).include?(ty)
    rst = spec['stages'] == 'all' ? stages : spec['stages'] & stages
    rst.each { |st| configs << { state: 'waiting', stage: st, type: ty, reason: r } }
  end
  %w[done dropped].each { |s| configs << { state: s, stage: nil, type: ty } }
end

rows = sm['step_table']
def row_matches?(row, c, groups, types)
  f = row['from']
  return false unless as_list(f['state']).include?(c[:state])
  return false if f['reason'] && f['reason'] != c[:reason]
  return false unless expand_types(f['types'], groups, types).include?(c[:type])
  if c[:stage]
    st = f['stages'] == 'all' ? nil : f['stages']
    return false if st && !st.include?(c[:stage])
  end
  true
end

events = (rows.map { |r| r['event'] } + sm['ignored'].flat_map { |i| i['events'].is_a?(Array) ? i['events'] : [] }).uniq
exit_events_by_reason = Hash.new { |h, k| h[k] = [] }
rows.each { |r| exit_events_by_reason[r['from']['reason']] << r['event'] if r['from']['reason'] }
running_events = rows.select { |r| as_list(r['from']['state']).include?('running') }.map { |r| r['event'] }.uniq

def ignored?(c, ev, sm, exit_events_by_reason, running_events)
  sm['ignored'].any? do |ig|
    if ig['states']
      ig['states'].include?(c[:state]) && (ig['events'] == 'all' || ig['events'].include?(ev))
    elsif ig['rule'] == 'waiting_foreign_exit'
      c[:state] == 'waiting' && exit_events_by_reason.any? { |r, evs| r != c[:reason] && evs.include?(ev) } && !exit_events_by_reason[c[:reason]].include?(ev)
    elsif ig['rule'] == 'running_foreign_result'
      c[:state] == 'running' && running_events.include?(ev)
    end
  end
end

# ---- guard space ----
def guard_dims(g)
  return {} unless g
  out = {}
  g.each do |k, v|
    case k
    when 'counter', 'req_counter' then out["#{k}:#{v['name']}"] = v['is']
    else out[k] = v
    end
  end
  out
end
DIM_VALUES = { 'hold' => %w[set none], 'deps' => %w[done pending], 'lease' => %w[ok wait], 'tier' => %w[R1 R2 R3],
               'single_step' => %w[one many], 'decision' => d['guard_enums']['step_decision'] }
# enums come from the spec, never from the rows themselves (a row-derived enum hides missing values)
def dim_values(dim, rows_dims)
  return %w[below at_cap] if dim.start_with?('counter:', 'req_counter:')
  DIM_VALUES.fetch(dim) { raise "guard dim without declared enum: #{dim}" }
end

esc = sm['escalate']
def expand_row(row, esc)
  return [row] unless row['to']['state'] == 'escalate'
  esc.map do |e|
    g = (row['guard'] || {}).merge(e['guard'])
    row.merge('guard' => g, 'to' => e['to'], 'effects' => (row['effects'] || []) + e['effects'], 'id' => row['id'] + '+esc')
  end
end
xrows = rows.flat_map { |r| expand_row(r, esc) }

def valid_target?(c, to, d, groups, types)
  case to['state']
  when 'done', 'dropped' then true
  when 'queued'
    st = case to['stage'] when 'same' then c[:stage] when 'first_stage' then (groups['work'].include?(c[:type]) ? d['first_stage']['work'] : d['first_stage']['integrate']) when 'resume_stage' then :resume else to['stage'] end
    st == :resume || d['types'][c[:type]]['stages'].include?(st)
  when 'running'
    rs = groups['work'].include?(c[:type]) ? d['running_stages']['work'] : d['running_stages']['integrate']
    rs.include?(to['stage'] == 'same' ? c[:stage] : to['stage'])
  when 'waiting'
    spec = d['waiting_reasons'][to['reason']] or return false
    expand_types(spec['types'], groups, types).include?(c[:type]) && (spec['stages'] == 'all' || spec['stages'].include?(c[:stage]))
  else false
  end
end

configs.each do |c|
  events.each do |ev|
    m = xrows.select { |r| r['event'] == ev && row_matches?(r, c, groups, TYPES) }
    if m.empty?
      errors << "uncovered: #{c} event=#{ev}" unless ignored?(c, ev, sm, exit_events_by_reason, running_events)
      next
    end
    # guard exhaustiveness
    rds = m.map { |r| guard_dims(r['guard']) }
    dims = rds.flat_map(&:keys).uniq
    space = dims.map { |dm| dim_values(dm, rds) }
    combos = space.empty? ? [[]] : space[0].product(*space[1..])
    combos.each do |combo|
      assign = dims.zip(combo).to_h
      hit = m.find { |r| guard_dims(r['guard']).all? { |k, v| assign[k] == v } }
      errors << "guard gap: #{c} event=#{ev} #{assign}" unless hit
      next unless hit
      errors << "invalid target: #{hit['id']} from #{c} -> #{hit['to']}" unless valid_target?(c, hit['to'], d, groups, TYPES)
    end
  end
  # exits
  next unless %w[queued running waiting].include?(c[:state])
  exits = xrows.select { |r| row_matches?(r, c, groups, TYPES) && !(r['to']['state'] == c[:state] && (r['to']['stage'] || 'same') == 'same' && r['to']['reason'] == c[:reason]) }
  errors << "no exit: #{c}" if exits.empty?
end

# inc must be guarded by below of the same counter (cap reachable), except exempt
EXEMPT = %w[access_denied]
xrows.each do |r|
  (r['effects'] || []).grep(/^inc:/).each do |e|
    name = e.split(':')[1]
    next if EXEMPT.include?(name)
    g = guard_dims(r['guard'])
    ok = g["counter:#{name}"] == 'below' || g["req_counter:#{name}"] == 'below'
    errors << "unguarded inc: #{r['id']} #{e}" unless ok
  end
end

# req hold table
rh = sm['req_hold']
vals = rh['values']
rh_rows = rh['table']
ro = rh['rescue_or_owner']
rh_events = rh_rows.map { |r| r['event'] }.uniq
vals.each do |v|
  rh_events.each do |ev|
    m = rh_rows.select { |r| as_list(r['from']).include?(v) && r['event'] == ev }
    next if m.empty?
    rds = m.map { |r| guard_dims(r['guard']) }
    dims = rds.flat_map(&:keys).uniq
    ge = d['guard_enums']
    space = dims.map { |dm| dm == 'answer' ? ge['answer'] : (dm == 'decision' ? ge['req_decision'] : dim_values(dm, rds)) }
    combos = space.empty? ? [[]] : space[0].product(*space[1..])
    combos.each do |combo|
      assign = dims.zip(combo).to_h
      errors << "req guard gap: #{v} #{ev} #{assign}" unless m.any? { |r| guard_dims(r['guard']).all? { |k, x| assign[k] == x } }
    end
  end
  next if v == 'none'
  ex = rh_rows.select { |r| as_list(r['from']).include?(v) && r['to'] != v }
  errors << "req hold no exit: #{v}" if ex.empty?
end
%w[replan_trigger rescue_trigger owner_trigger].each do |ev|
  vals.each do |v|
    next if %w[owner parked].include?(v) # handled by precedence (pending_triggers)
    has = rh_rows.any? { |r| as_list(r['from']).include?(v) && r['event'] == ev }
    errors << "req trigger unhandled: #{v} #{ev} (precedence note covers only lower-priority)" if !has && !(v == 'rescue' && ev == 'replan_trigger')
  end
end

# ---- every capped counter must be incremented somewhere (catches guard-only counters) ----
all_effects = xrows.flat_map { |r| r['effects'] || [] } + rh_rows.flat_map { |r| r['effects'] || [] } +
              ro.flat_map { |r| r['effects'] || [] } + esc.flat_map { |r| r['effects'] || [] }
(d['counters'].keys + d['req_counters'].keys).each do |name|
  errors << "counter never incremented: #{name}" unless all_effects.include?("inc:#{name}")
end

# ---- self-loops (to == from) must increment a capped counter guarded by below ----
xrows.each do |r|
  f = r['from']; t = r['to']
  next unless t['state'] == 'waiting' && as_list(f['state']) == ['waiting'] && t['reason'] == f['reason']
  incs = (r['effects'] || []).grep(/^inc:/)
  errors << "unbounded self-loop: #{r['id']}" if incs.empty?
end

# ---- req hold table: full (hold, event) coverage ----
rh_ign = rh['ignored'] || []
rh_all_events = (rh_events + rh_ign.flat_map { |i| i['events'] }).uniq
vals.each do |v|
  rh_all_events.each do |ev|
    next if rh_rows.any? { |r| as_list(r['from']).include?(v) && r['event'] == ev }
    next if rh_ign.any? { |i| i['from'].include?(v) && i['events'].include?(ev) }
    errors << "req uncovered: hold=#{v} event=#{ev}"
  end
end

# ---- protocol cross-check: rescuer decision enums must equal the guard enums ----
pr = YAML.load_file(File.join(File.dirname(ARGV[0] || 'spec/design/state-machine.yaml'), 'protocol-run.yaml'))
dec = pr['files'].find { |f| f['id'] == 'PF-RESULT' }['fields'].find { |f| f['name'] == 'decision' }['type']
step_enum = dec[/step 级：enum ([a-z_|]+)/, 1].to_s.split('|')
req_enum  = dec[/需求级：enum ([a-z_|]+)/, 1].to_s.split('|')
ge = d['guard_enums']
errors << "protocol step_decision mismatch: #{step_enum} vs #{ge['step_decision']}" unless step_enum.sort == ge['step_decision'].sort
errors << "protocol req_decision mismatch: #{req_enum} vs #{ge['req_decision']}" unless req_enum.sort == ge['req_decision'].sort

puts "configs=#{configs.size} events=#{events.size} rows=#{rows.size} (expanded #{xrows.size})"
if errors.empty?
  puts 'OK'
else
  puts errors.uniq.first(80)
  puts "#{errors.uniq.size} errors"
  exit 1
end
