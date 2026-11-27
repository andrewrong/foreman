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
               'single_step' => %w[one many], 'budget' => %w[ok over], 'decision' => d['guard_enums']['step_decision'] }
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

# ---- self-loops (to == from, any state) must increment a capped counter ----
xrows.each do |r|
  f = r['from']; t = r['to']
  next unless as_list(f['state']) == [t['state']] && %w[waiting running].include?(t['state'])
  next unless t['reason'] == f['reason'] && (t['stage'].nil? || t['stage'] == 'same')
  next if t['state'] == 'running' && f['stages'] != 'all' && f['stages'].size != 1
  incs = (r['effects'] || []).grep(/^inc:/)
  errors << "unbounded self-loop: #{r['id']}" if incs.empty?
end

# ---- explicit ignore entries must not overlap a row (a row would be silently shadowed) ----
configs.each do |c|
  sm['ignored'].select { |ig| ig['states'] }.each do |ig|
    next unless ig['states'].include?(c[:state])
    evs = ig['events'] == 'all' ? events : ig['events']
    evs.each do |ev|
      hit = xrows.find { |r| r['event'] == ev && row_matches?(r, c, groups, TYPES) }
      errors << "ignored overlaps row: #{hit['id']} #{c} event=#{ev}" if hit
    end
  end
end
(rh['ignored'] || []).each do |ig|
  ig['from'].each do |v|
    ig['events'].each do |ev|
      hit = rh_rows.find { |r| as_list(r['from']).include?(v) && r['event'] == ev }
      errors << "req ignored overlaps row: #{hit['id']} hold=#{v} event=#{ev}" if hit
    end
  end
end

# ---- multi-step cycles: every strongly connected set of configs must contain an inc of a counter
#      that no edge inside the set resets. Waits on external clocks (deps, quota, hold) are exempt. ----
node = ->(c) { [c[:state], c[:stage], c[:type], c[:reason]] }
edges = Hash.new { |h, k| h[k] = [] }
configs.each do |c|
  next unless %w[queued running waiting].include?(c[:state])
  xrows.select { |r| row_matches?(r, c, groups, TYPES) }.each do |r|
    next if (r['effects'] || []).include?('alert_starved')   # S09: starvation alarm timer, exit is dispatch
    t = r['to']
    next unless %w[queued running waiting].include?(t['state'])
    stage = case t['stage'] when nil, 'same' then c[:stage] when 'first_stage' then (groups['work'].include?(c[:type]) ? d['first_stage']['work'] : d['first_stage']['integrate']) when 'resume_stage' then next else t['stage'] end
    edges[node.(c)] << [[t['state'], stage, c[:type], t['reason']], r]
  end
end
EXEMPT_REASONS = %w[deps quota hold]
# events that are the result of a Run (a counter reset "on any result" happens only on these)
RESULT_EVENTS = %w[run_ok check_pass check_fail review_approve review_changes merge_back_conflict merged merge_conflict spec_conflict bad_result budget_exceeded access_request l1_pass l1_fail]
index = {}; low = {}; stack = []; on = {}; sccs = []; i = 0
strong = lambda do |v|
  index[v] = low[v] = i; i += 1; stack << v; on[v] = true
  edges[v].each do |(w, _)|
    if !index.key?(w) then strong.(w); low[v] = [low[v], low[w]].min
    elsif on[w] then low[v] = [low[v], index[w]].min end
  end
  if low[v] == index[v]
    comp = []; loop { w = stack.pop; on[w] = false; comp << w; break if w == v }
    sccs << comp
  end
end
edges.keys.each { |v| strong.(v) unless index.key?(v) }
counter_reset = d['counters'].transform_values { |v| Array(v['reset']) }
# A counter bounds a cycle only if no edge of that cycle resets it. Approximation per SCC: an edge is
# "bounding" if it increments a counter that no edge in the SCC resets. Remove bounding edges and look
# again: any cycle left (outside exempt external waits) has no bound.
def resets_of(r)
  (r['effects'] || []).grep(/^reset:/).flat_map { |e| e.split(':', 2)[1].split(',') }
end
def tarjan(nodes, adj)
  index = {}; low = {}; stack = []; on = {}; out = []; i = 0
  visit = lambda do |v|
    index[v] = low[v] = i; i += 1; stack << v; on[v] = true
    adj[v].each do |(w, _)|
      next unless nodes[w]
      if !index.key?(w) then visit.(w); low[v] = [low[v], low[w]].min
      elsif on[w] then low[v] = [low[v], index[w]].min end
    end
    if low[v] == index[v]
      comp = []; loop { w = stack.pop; on[w] = false; comp << w; break if w == v }
      out << comp
    end
  end
  nodes.keys.each { |v| visit.(v) unless index.key?(v) }
  out
end
# Sound iterative rule: inside a strongly connected set, an edge that increments a counter which no
# edge of that set resets lies only on bounded cycles, so drop it and re-examine what remains (the
# remaining, smaller sets may have lost the edges that used to reset other counters).
check_scc = lambda do |comp, adj_in|
  set = comp.to_h { |x| [x, true] }
  inner = comp.flat_map { |v| adj_in[v].select { |(w, _)| set[w] }.map { |(_, r)| r } }
  next if inner.empty?
  inner_events = inner.map { |r| r['event'] }
  inner_resets = inner.flat_map { |r| resets_of(r) }
  reset_inside = lambda do |cn|
    rs = counter_reset[cn] || []
    (rs & inner_events).any? || inner_resets.include?(cn) ||
      (rs.include?('any_valid_result') && inner_events.any? { |e| RESULT_EVENTS.include?(e) && e != 'bad_result' }) ||
      (rs.include?('any_non_infra_result') && inner_events.any? { |e| RESULT_EVENTS.include?(e) })
  end
  bounding = ->(r) { (r['effects'] || []).grep(/^inc:/).any? { |e| !reset_inside.(e.split(':')[1]) } }
  adj = Hash.new { |h, k| h[k] = [] }
  comp.each { |v| adj_in[v].each { |(w, r)| adj[v] << [w, r] if set[w] && !bounding.(r) } }
  live = comp.reject { |n| n[0] == 'waiting' && EXEMPT_REASONS.include?(n[3]) }.to_h { |x| [x, true] }
  removed_any = inner.size != comp.sum { |v| adj[v].count { |(w, _)| set[w] } }
  tarjan(live, adj).each do |sub|
    s2 = sub.to_h { |x| [x, true] }
    loop_rows = sub.flat_map { |v| adj[v].select { |(w, _)| s2[w] }.map { |(_, r)| r['id'] } }.uniq
    next if loop_rows.empty?
    if removed_any
      check_scc.(sub, adj)          # smaller set: re-evaluate resets with only its own edges
    else
      errors << "unbounded cycle: #{loop_rows.sort.join(',')}"
    end
  end
end
sccs.each { |comp| check_scc.(comp, edges) }

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
