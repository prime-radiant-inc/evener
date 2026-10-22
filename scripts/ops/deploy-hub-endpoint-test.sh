#!/usr/bin/env bash
# Command-boundary tests of the actual launchd worktree deploy script.
# No real launchctl, build, SSH or hub is invoked. Requires Python 3 and Node.
set -euo pipefail
case "${1:-}" in
-h|--help) printf '%s\n' 'Usage: bash scripts/ops/deploy-hub-endpoint-test.sh' 'Runs disposable strict command-boundary tests, not a macOS end-to-end deployment.'; exit 0 ;;
'') ;;
*) exit 2 ;;
esac
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
python3 - "$script_dir/deploy-hub.sh" <<'PY'
import json
import os
from pathlib import Path
import secrets
import shutil
import signal
import stat
import subprocess
import sys
import tempfile

# Copy exact inputs before running. Cases own private HOME, repo/config/state,
# and a closed PATH. Real git only seeds/build-identifies the disposable repo.
source = Path(sys.argv[1]).read_bytes()
def interrupted(*_):
    raise KeyboardInterrupt
for signum in [signal.SIGTERM,signal.SIGHUP]: signal.signal(signum,interrupted)
native_ps=shutil.which('ps')
def invoke(argv,p,env):
    proc=subprocess.Popen(argv,cwd=p/'repo',env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,start_new_session=True)
    timed_out=False; leaks=[]
    try:
        try: out,err=proc.communicate(timeout=20)
        except subprocess.TimeoutExpired:
            timed_out=True
            os.killpg(proc.pid,signal.SIGKILL)
            out,err=proc.communicate(timeout=2)
        rows=[json.loads(line) for line in (p/'calls.jsonl').read_text().splitlines()] if (p/'calls.jsonl').exists() else []
        pids={row['pid'] for row in rows}
        if (p/'orphan_pid').exists(): pids.add(int((p/'orphan_pid').read_text()))
        if pids:
            probe=subprocess.run([native_ps,'-p',','.join(map(str,pids)),'-o','pid=,stat='],capture_output=True,text=True,timeout=2)
            assert probe.returncode in [0,1], 'owned process cleanup inspection failed'
            leaks=[line for line in probe.stdout.splitlines() if len(line.split())==2 and not line.split()[1].startswith('Z')]
        return proc.returncode,out,err,timed_out,leaks
    finally:
        # Stop ONLY this invocation and groups minted by its command wrappers.
        # This also cleans a defective script on timeout or an interrupt.
        groups={proc.pid}
        if (p/'calls.jsonl').exists():
            for line in (p/'calls.jsonl').read_text().splitlines():
                row=json.loads(line)
                if row['group']==row['pid']: groups.add(row['group'])
        for group in groups:
            try: os.killpg(group,signal.SIGKILL)
            except ProcessLookupError: pass
        proc.communicate(timeout=2)
fixture = r'''#!/usr/bin/env python3
import json, os, pathlib, subprocess, sys, time
p = pathlib.Path(os.environ['FIXTURE'])
f = json.loads((p/'facts.json').read_text())
cmd, args = pathlib.Path(sys.argv[0]).name, sys.argv[1:]
s = json.loads((p/'state.json').read_text())
with (p/'calls.jsonl').open('a') as out:
    call={'cmd':cmd, 'args':args, 'pid':os.getpid(), 'group':os.getpgrp()}
    if cmd=='evener':
        call['env']={k:v for k,v in os.environ.items() if k in ['HOME','XDG_CONFIG_HOME','XDG_STATE_HOME','EVENER_HUB_LISTEN_ADDR','EVENER_HUB_ADDR','EVENER_HUB_URL','EVENER_PROVIDERS_CONFIG','PRIVATE_SENTINEL']}
        call['cwd']=os.getcwd()
    out.write(json.dumps(call)+'\n')
def refuse():
    (p/'violation').write_text(json.dumps([cmd,args]))
    sys.exit(97)
def save():
    pending=p/('state-next-'+str(os.getpid())+'.json')
    pending.write_text(json.dumps(s))
    pending.replace(p/'state.json')
def service_cwd():
    if f.get('cwd_space_alias'): return p/'space-alias'
    if f.get('cwd_space_literal'): return p/'physical '
    return p/'service-alias' if f.get('cwd_alias') else (p/'service' if f.get('service_cwd') else p/'repo')
def config_selector():
    if f.get('config_newline_link'): return str(p/'newline-config-alias.toml')
    if f.get('config_newline_parent'): return str(p/'newline-parent-alias/selected.toml')
    if f.get('config_space'): return str(p/'space-config-alias.toml')
    return {'alias':str(p/'alias.toml'),'hardlink':str(p/'hardlink.toml'),'relative':'../alias.toml','dotdot':'../link/../../selected.toml','different':'../link/../selected.toml'}.get(f.get('config_selector'),str(p/'selected.toml'))
def program(): return str(p/'alias-bin/evener' if f.get('program_alias') else (p/'other/evener' if f.get('different_program') else p/'repo/evener'))
def argv():
    flags=[] if f.get('default_config') else ['--config='+config_selector()]
    if not f.get('env_listener') and not f.get('toml_listener'): flags+=['--addr='+f.get('listener','[::]:0')]
    if f.get('separate_flags'):
        flags=[x for word in flags for x in [word.split('=',1)[0],word.split('=',1)[1]]]
    if f.get('subcommand'): flags=['endpoint','--json']
    return [program(),'hub']+flags
def endpoint_args():
    flags=[] if f.get('default_config') else ['--config',config_selector()]
    if not f.get('env_listener') and not f.get('toml_listener'): flags+=['--addr',f.get('listener','[::]:0')]
    return ['hub','endpoint','--json']+flags
def live_argv():
    words=argv()
    if not f.get('inplace') or s.get('restarted'): return words
    bound=f.get('bound_host','[::]')+':'+('19472' if f.get('inplace_mismatch') else '19471')
    index=next((i for i,w in enumerate(words) if w in ['-addr','--addr'] or w.startswith(('-addr=','--addr='))),None)
    if index is None: words+=['-addr',bound]
    elif '=' in words[index]: words[index]=words[index].split('=',1)[0]+'='+bound
    else: words[index+1]=bound
    if f.get('inplace_extra'): words+=['--public-url=https://other.invalid']
    if f.get('inplace_duplicate'): words+=['-addr',bound]
    return words
if cmd == 'launchctl':
    if args == ['list']:
        print('PID\tStatus\tLabel\n101\t0\tcom.test.evener-selected')
        if not f.get('auto_single'): print('202\t0\tcom.test.evener-other')
    elif args == ['print', 'gui/'+str(os.getuid())+'/com.test.evener-selected']:
        if s['built'] and f.get('vanished'): sys.exit(1)
        if f.get('pid_change_before_build') and s.get('reports'): s['pid']=404; save()
        executable=program()
        if f.get('wrong_program'): executable=str(p/'other/evener')
        print('gui/test = {\n\tprogram = '+executable+'\n\tworking directory = '+str(service_cwd()))
        print('\targuments = {')
        for arg in argv(): print('\t\t'+arg)
        print('\t}\n\tinherited environment = {\n\t\tHOME => '+str(p/'home')+'\n\t\tXDG_CONFIG_HOME => '+str(p/'config')+'\n\t\tXDG_STATE_HOME => '+str(p/'state')+'\n\t}')
        print('\tenvironment = {\n\t\tPRIVATE_SENTINEL => SECRET_DO_NOT_PRINT')
        if f.get('env_listener') or f.get('flag_and_env'): print('\t\tEVENER_HUB_LISTEN_ADDR => 0.0.0.0:0')
        if f.get('empty_listener'): print('\t\tEVENER_HUB_LISTEN_ADDR => ')
        if f.get('retired_service_env'): print('\t\tEVENER_HUB_ADDR => SECRET_DO_NOT_PRINT')
        if s['built'] and f.get('definition_change'): print('\t\tXDG_STATE_HOME => '+str(p/'other-state'))
        print('\t}')
        if s['pid']: print('\tpid = '+str(s['pid']))
        print('}')
    elif args == ['print', 'gui/'+str(os.getuid())+'/com.test.evener-other']:
        print('gui/other = {\n\tprogram = '+str(p/'repo/evener')+'\n\tworking directory = '+str(p/'repo'))
        print('\targuments = {\n\t\t'+str(p/'repo/evener')+'\n\t\thub\n\t\t--config='+str(p/'other.toml')+'\n\t}')
        print('\tenvironment = {\n\t\tHOME => '+str(p/'home')+'\n\t\tXDG_STATE_HOME => '+str(p/'other-state')+'\n\t}\n\tpid = 202\n}')
    elif args == ['kickstart','-k','gui/'+str(os.getuid())+'/com.test.evener-selected']:
        s['pid']=0 if f.get('stopped_after_kick') else (101 if f.get('unchanged_process') else 303)
        s['restarted']=True
        save()
        if f.get('kick_fail'): sys.exit(9)
    else: refuse()
elif cmd == 'make':
    if args != ['build-hub']: refuse()
    print(f['build_stdout_marker'],flush=True)
    print(f['build_stderr_marker'],file=sys.stderr,flush=True)
    private_stream=sys.stdout if f.get('build_stdout_large') else sys.stderr
    print(f['build_private_marker'],file=private_stream,flush=True)
    if f.get('build_stdout_large') or f.get('build_stderr_large'):
        stream=sys.stdout if f.get('build_stdout_large') else sys.stderr
        stream.write('x'*(8*1024*1024+1024)); stream.flush()
    if f.get('build_hang'): time.sleep(60)
    if f.get('build_fail'): sys.exit(7)
    # Boundary-only atomic replacement of the disposable executable symlink.
    replacement=p/'repo/next'; replacement.symlink_to(p/'dispatch'); replacement.replace(p/'repo/evener')
    s['built']=True; save()
    if f.get('toml_edit'):
        (p/'selected.toml').write_text('addr = "0.0.0.0:0"\n')
elif cmd == 'ps':
    pid=str(s['pid'])
    if f.get('generation_change') and s['built'] and s['pid']==101: birth='Fri Oct 16 08:01:00 2026'
    else: birth='Fri Oct 16 08:00:00 2026'
    if args==['-p',pid,'-o','uid=']: print(os.getuid()+1 if f.get('wrong_user') else os.getuid())
    elif args==['-p',pid,'-o','lstart=']: print(birth)
    elif args==['-p',pid,'-ww','-o','command=']: print(' '.join(live_argv())+(' unexpected' if f.get('wrong_argv') else ''))
    else: refuse()
elif cmd == 'lsof':
    if args==['-a','-p',str(s['pid']),'-d','cwd','-Fn']: print('p'+str(s['pid'])+'\nn'+str(p/'other' if f.get('wrong_cwd') else (p/'physical' if f.get('cwd_space_collision') else service_cwd().resolve())))
    elif args[:4]==['-a','-p',str(s['pid']),'-Fn'] and args[4:] in [[str(p/'state/evener/hub.lock')],[str(p/'other-state/evener/hub.lock')],[str(p/'custom-root/hub.lock')]]:
        lock=p/'custom-root/hub.lock' if f.get('custom_root') else p/'state/evener/hub.lock'
        if f.get('missing_root_evidence') or args[4]!=str(lock): sys.exit(1)
        print('p'+str(s['pid'])+'\nn'+str(lock))
    elif args==['-a','-p',str(s['pid']),'-d','txt','-F','Di']:
        st=(p/'repo/evener').stat()
        print('p'+str(s['pid'])+'\nftxt\nD0x'+format(st.st_dev,'x')+'\ni'+str(st.st_ino+(1 if f.get('old_image_mismatch') else 0)))
    else: refuse()
elif cmd == 'evener':
    if args == ['launch-check','--json']:
        if f.get('capability_invalid'): print('{not-json'); sys.exit(0)
        print(json.dumps({'hub_endpoint_schema':0 if f.get('unsupported') else 1,'version':'old','protocol':'evener-appwire-v7'}))
    elif args == endpoint_args():
        if f.get('empty_listener'):
            if os.environ.get('EVENER_HUB_LISTEN_ADDR')=='': sys.exit(1)
        if f.get('hang'): time.sleep(60)
        if f.get('orphan_pipe'):
            child=subprocess.Popen([sys.executable,'-c','import time; time.sleep(60)'])
            (p/'orphan_pid').write_text(str(child.pid))
            sys.exit(0)
        if f.get('legacy_endpoint'): sys.stderr.write('SECRET_DO_NOT_PRINT'); sys.exit(2)
        if f.get('stderr_large'): sys.stderr.write('SECRET_DO_NOT_PRINT'*4000)
        if f.get('invalid_json'): print('{not-json'); sys.exit(0)
        if f.get('array_json'): print('[]'); sys.exit(0)
        if f.get('secret_error'): sys.stderr.write('SECRET_DO_NOT_PRINT'); sys.exit(8)
        new=s.get('restarted',False) and s['pid']>0
        if new:
            s['successor_queries']=s.get('successor_queries',0)+1; save()
        config=p/'config/evener/hub.toml' if f.get('default_config') else p/'selected.toml'
        if f.get('config_newline_parent'): config=p/'config-parent/selected.toml'
        if f.get('config_space') and not f.get('config_space_collision'): config=p/'selected.toml '
        root=p/'other-state/evener' if (s['built'] and f.get('root_change')) or f.get('unavailable_wrong_root') else p/'state/evener'
        if f.get('custom_root'): root=p/'custom-root'
        source='env' if f.get('env_listener') else ('config' if f.get('toml_listener') else 'flag')
        report={'schema_version':2 if f.get('schema') else 1,'state':'running','config_path':str(config),'config_exists':True,'hub_state_root':str(root),'configured_listen_addr':f.get('listener','[::]:0'),'listen_source':source,'public_url':'https://publication.invalid'}
        unavailable=f.get('unavailable') or f.get('damaged') or f.get('unsupported') or f.get('old_image_mismatch') or f.get('capability_invalid') or f.get('unavailable_wrong_root') or f.get('missing_root_evidence')
        if not s['pid']: report.update(state='stopped')
        elif not new and unavailable: report.update(state='unavailable',reason='invalid_record' if f.get('damaged') else 'unreachable')
        elif new and f.get('port_refresh') and s['successor_queries']==1: report.update(state='unavailable',reason='unreachable')
        else:
            pid=s['pid'] if new else 101
            if f.get('wrong_pid') or (new and f.get('successor_wrong_pid')): pid=202
            boot='new-boot' if new else 'old-boot'
            if new and f.get('old_boot'): boot='old-boot'
            s['reports']=s.get('reports',0)+1; save()
            port=19472+s['reports'] if new else 19471
            local='http://'+f.get('local_host','[::1]')+':'+str(port)
            running_config=str(config)
            if f.get('running_alias'): running_config=str(p/'alias.toml')
            elif f.get('running_hardlink'): running_config=str(p/'hardlink.toml')
            elif f.get('running_relative'): running_config='../alias.toml'
            report['running']={'schema_version':1,'pid':pid,'boot_id':boot,'started_at':'2026-10-16T08:00:00Z','config_path':running_config,'hub_state_root':str(root),'requested_listen_addr':f.get('listener','[::]:0'),'listen_source':source,'bound_addr':f.get('bound_host','[::]')+':'+str(port),'local_url':local,'loopback_url':local,'version':f['version'] if new else 'old','backend_git_sha':f['sha'] if new else 'old'}
            if f.get('inplace') and not new:
                report['running'].update(requested_listen_addr=report['running']['bound_addr'],listen_source='flag')
            if new and f.get('stale'): report['running'].update(pid=101,boot_id='old-boot')
            if new and f.get('wrong_build'): report['running']['version']='wrong'
            if new and f.get('wrong_sha'): report['running']['backend_git_sha']='wrong'
            if new and f.get('missing_build'): report['running'].pop('version')
            if new and f.get('missing_boot'): report['running'].pop('boot_id')
            if new and f.get('running_wrong_root'): report['running']['hub_state_root']=str(p/'other-state/evener')
            if new and f.get('running_wrong_config'): report['running']['config_path']=str(p/'other.toml')
            if new and f.get('unrelated_report'):
                report.update(config_path=str(p/'other.toml'),hub_state_root=str(p/'other-state/evener'))
                report['running'].update(pid=202,config_path=str(p/'other.toml'),hub_state_root=str(p/'other-state/evener'),boot_id='other-boot')
            if f.get('injection'): report['running']['boot_id']=('new-' if new else 'old-')+'$(touch '+str(p/'executed')+')'
        if f.get('bad_state'): report['state']='unknown'
        if f.get('wrong_config'): report['config_path']=str(p/'other.toml')
        if f.get('toml_edit') and s['built']:
            # The test-owned edit changes config immediately, the running
            # listener only after restart. Declare these facts, do not parse TOML.
            report['configured_listen_addr']='0.0.0.0:0'
            report.pop('public_url')
            if new:
                report['running'].update(requested_listen_addr='0.0.0.0:0',bound_addr='0.0.0.0:'+str(port),local_url='http://127.0.0.1:'+str(port),loopback_url='http://127.0.0.1:'+str(port))
        if f.get('stdout_large'): report['padding']='x'*70000
        with (p/'reports.jsonl').open('a') as reports: reports.write(json.dumps(report)+'\n')
        print(json.dumps(report))
    else: refuse()
elif cmd == 'curl':
    # Permit only the legacy command for the recorded red run, NEVER network.
    if args != ['-fsS','--max-time','3','http://127.0.0.1:9180/api/health']: refuse()
    print(json.dumps({'version':f['version'],'started_at':'2026-10-16T08:00:00Z'}))
else: refuse()
'''
# Independent expected outcomes and effects, not generated from production logic.
# Last column: (build calls, selected kickstart calls).
cases=[
 ('selected_endpoint',{},[],True,(1,1)),
 ('inplace_flag',{'inplace':True},[],True,(1,1)),
 ('inplace_space_flag',{'inplace':True,'separate_flags':True},[],True,(1,1)),
 ('inplace_config',{'inplace':True,'toml_listener':True},[],True,(1,1)),
 ('inplace_environment',{'inplace':True,'env_listener':True,'listener':'0.0.0.0:0'},[],True,(1,1)),
 ('inplace_fixed_change',{'inplace':True,'listener':'127.0.0.1:9180'},[],False,(0,0)),
 ('inplace_no_ephemeral_evidence',{'inplace':True,'toml_listener':True,'listener':'127.0.0.1:9180'},[],False,(0,0)),
 ('inplace_non_listener',{'inplace':True,'inplace_extra':True},[],False,(0,0)),
 ('inplace_bound_mismatch',{'inplace':True,'inplace_mismatch':True},[],False,(0,0)),
 ('inplace_scoped_bound',{'inplace':True,'bound_host':'[fe80::1%lo0]'},[],False,(0,0)),
 ('inplace_duplicate',{'inplace':True,'inplace_duplicate':True},[],False,(0,0)),
 ('inplace_unavailable',{'inplace':True,'unavailable':True},[],False,(0,0)),
 ('live_cwd_symlink',{'cwd_alias':True},[],True,(1,1)),
 ('stopped_cwd_symlink',{'cwd_alias':True,'stopped':True},[],True,(1,1)),
 ('different_cwd_symlink',{'cwd_alias':True,'wrong_cwd':True},[],False,(0,0)),
 ('program_symlink',{'program_alias':True},[],True,(1,1)),
 ('different_program_inode',{'different_program':True},[],False,(0,0)),
 ('live_cwd_space_alias',{'cwd_space_alias':True},[],True,(1,1)),
 ('stopped_cwd_space_alias',{'cwd_space_alias':True,'stopped':True},[],True,(1,1)),
 ('live_cwd_space_literal',{'cwd_space_literal':True},[],True,(1,1)),
 ('stopped_cwd_space_literal',{'cwd_space_literal':True,'stopped':True},[],True,(1,1)),
 ('live_cwd_space_collision',{'cwd_space_alias':True,'cwd_space_collision':True},[],False,(0,0)),
 ('stopped_cwd_space_collision',{'cwd_space_alias':True,'cwd_space_collision':True,'stopped':True},[],False,(1,1)),
 ('config_path_space',{'config_space':True},[],True,(1,1)),
 ('config_path_space_collision',{'config_space':True,'config_space_collision':True},[],False,(0,0)),
 ('config_newline_link_collision',{'config_newline_link':True},[],False,(0,0)),
 ('config_newline_parent_collision',{'config_newline_parent':True},[],False,(0,0)),
 ('service_config_symlink',{'config_selector':'alias'},[],True,(1,1)),
 ('explicit_config_symlink',{},['--config','@alias'],True,(1,1)),
 ('explicit_config_hardlink',{},['--config','@hardlink'],True,(1,1)),
 ('relative_explicit_config_hardlink',{'service_cwd':True},['--config','../hardlink.toml'],True,(1,1)),
 ('service_config_hardlink',{'config_selector':'hardlink'},[],True,(1,1)),
 ('running_config_hardlink',{'running_hardlink':True},[],True,(1,1)),
 ('explicit_config_equal_content',{},['--config','@equal-content'],False,(0,0)),
 ('running_config_symlink',{'running_alias':True},[],True,(1,1)),
 ('relative_service_config',{'config_selector':'relative','service_cwd':True},[],True,(1,1)),
 ('relative_explicit_config',{'service_cwd':True},['--config','../alias.toml'],True,(1,1)),
 ('relative_running_config',{'running_relative':True,'service_cwd':True},[],True,(1,1)),
 ('symlink_dotdot_same',{'config_selector':'dotdot','service_cwd':True},[],True,(1,1)),
 ('symlink_dotdot_different',{'config_selector':'different','service_cwd':True},[],False,(0,0)),
 ('two_labels_shared_executable',{},[],True,(1,1)),
 ('dirty_worktree',{'dirty':True},[],True,(1,1)),
 ('custom_state_root',{'custom_root':True,'unavailable':True},[],True,(1,1)),
 ('explicit_config',{},['--config','@config'],True,(1,1)),
 ('separate_service_flags',{'separate_flags':True},[],True,(1,1)),
 ('default_config',{'default_config':True,'toml_listener':True},[],True,(1,1)),
 ('default_explicit_config',{'default_config':True},['--config','@default'],True,(1,1)),
 ('wildcard_ipv4',{'listener':'0.0.0.0:0','bound_host':'0.0.0.0','local_host':'127.0.0.1'},[],True,(1,1)),
 ('ipv6',{'listener':'[::]:0'},[],True,(1,1)),
 ('port_zero_refresh',{'port_refresh':True},[],True,(1,1)),
 ('environment_listener',{'env_listener':True,'listener':'0.0.0.0:0','bound_host':'0.0.0.0','local_host':'127.0.0.1'},[],True,(1,1)),
 ('flag_over_environment',{'flag_and_env':True},[],True,(1,1)),
 ('intentional_toml_edit',{'toml_listener':True,'toml_edit':True},[],True,(1,1)),
 ('unresponsive',{'unavailable':True},[],True,(1,1)),
 ('damaged',{'damaged':True},[],True,(1,1)),
 ('unavailable_wrong_root',{'unavailable_wrong_root':True},[],False,(0,0)),
 ('missing_root_evidence',{'missing_root_evidence':True},[],False,(0,0)),
 ('stopped',{'stopped':True},[],True,(1,1)),
 ('failed_replacement_stopped_retry',{'stopped_after_kick':True,'retry_stopped':True},[],True,(2,2)),
 ('interrupted_kickstart',{'kick_fail':True},[],True,(1,1)),
 ('inert_shell_output',{'injection':True},[],True,(1,1)),
 ('auto_single',{'auto_single':True},['@auto'],True,(1,1)),
 ('auto_ambiguous',{},['@auto'],False,(0,0)),
 ('retired_addr',{},['--addr','127.0.0.1:9180'],False,(0,0)),
 ('retired_addr_equals',{},['--addr=127.0.0.1:9180'],False,(0,0)),
 ('config_mismatch',{},['--config','@other'],False,(0,0)),
 ('wrong_pid',{'wrong_pid':True},[],False,(0,0)),
 ('wrong_program',{'wrong_program':True},[],False,(0,0)),
 ('wrong_user',{'wrong_user':True},[],False,(0,0)),
 ('wrong_argv',{'wrong_argv':True},[],False,(0,0)),
 ('not_a_daemon',{'subcommand':True},[],False,(0,0)),
 ('pid_changed_before_build',{'pid_change_before_build':True},[],False,(0,0)),
 ('wrong_cwd',{'wrong_cwd':True},[],False,(0,0)),
 ('wrong_config',{'wrong_config':True},[],False,(0,0)),
 ('unsupported',{'unsupported':True},[],False,(0,0)),
 ('unsupported_legacy_cli',{'unsupported':True,'legacy_endpoint':True},[],False,(0,0)),
 ('unproved_old_image',{'old_image_mismatch':True},[],False,(0,0)),
 ('unknown_capability',{'unavailable':True,'capability_invalid':True},[],False,(0,0)),
 ('invalid_json',{'invalid_json':True},[],False,(0,0)),
 ('array_json',{'array_json':True},[],False,(0,0)),
 ('unsupported_schema',{'schema':True},[],False,(0,0)),
 ('invalid_state',{'bad_state':True},[],False,(0,0)),
 ('stdout_bound',{'stdout_large':True},[],False,(0,0)),
 ('stderr_bound',{'stderr_large':True},[],False,(0,0)),
 ('execution_bound',{'hang':True},[],False,(0,0)),
 ('descendant_pipe_bound',{'orphan_pipe':True},[],False,(0,0)),
 ('secret_safe_failure',{'secret_error':True},[],False,(0,0)),
 ('definition_changed',{'definition_change':True},[],False,(1,0)),
 ('root_changed',{'root_change':True},[],False,(1,0)),
 ('generation_changed',{'generation_change':True},[],False,(1,0)),
 ('service_vanished',{'vanished':True},[],False,(1,0)),
 ('build_failure',{'build_fail':True},[],False,(1,0)),
 ('build_stdout_bound',{'build_stdout_large':True},[],False,(1,0)),
 ('build_stderr_bound',{'build_stderr_large':True},[],False,(1,0)),
 ('build_deadline',{'build_hang':True},[],False,(1,0)),
 ('build_diagnostic_creation_failure',{'bad_tmpdir':True},[],False,(0,0)),
 ('wrong_successor_pid',{'successor_wrong_pid':True},[],False,(1,1)),
 ('unrelated_healthy_successor',{'unrelated_report':True},[],False,(1,1)),
 ('old_boot',{'old_boot':True},[],False,(1,1)),
 ('unchanged_process',{'unchanged_process':True},[],False,(1,1)),
 ('stale_generation',{'stale':True},[],False,(1,1)),
 ('wrong_build',{'wrong_build':True},[],False,(1,1)),
 ('wrong_sha',{'wrong_sha':True},[],False,(1,1)),
 ('missing_build',{'missing_build':True},[],False,(1,1)),
 ('missing_boot',{'missing_boot':True},[],False,(1,1)),
 ('wrong_successor_root',{'running_wrong_root':True},[],False,(1,1)),
 ('wrong_successor_config',{'running_wrong_config':True},[],False,(1,1)),
 ('stopped_after_restart',{'stopped_after_kick':True},[],False,(1,1)),
 ('no_node',{'no_node':True},[],False,(0,0)),
 ('no_npm',{'no_npm':True},[],False,(0,0)),
 ('empty_service_listener',{'empty_listener':True},[],False,(0,0)),
 ('retired_service_environment',{'retired_service_env':True},[],False,(0,0)),
 ('help_without_node',{'no_node':True},['@help'],True,(0,0)),
]
for option in ['--label','--config','--timeout']:
    for value in [None,'','--help']:
        cases.append(('missing_'+option[2:]+'_'+str(value),{},['@raw',option]+([] if value is None else [value]),False,(0,0)))
for value in ['0','-1','x','1.5','999999999999999999999999']:
    cases.append(('timeout_'+value,{},['--timeout',value],False,(0,0)))
failures=[]
with tempfile.TemporaryDirectory(prefix='task7c-', dir=os.environ.get('EVENER_SCRATCH_DIR')) as root:
    root=Path(root)
    for name, facts, args, success, effects in cases:
        p=root/name; p.mkdir(); (p/'repo/scripts/ops').mkdir(parents=True); (p/'bin').mkdir(); (p/'home').mkdir(); (p/'state/evener').mkdir(parents=True); (p/'config/evener').mkdir(parents=True); (p/'other-state/evener').mkdir(parents=True)
        script=p/'repo/scripts/ops/deploy-hub.sh'; script.write_bytes(source)
        if facts.get('build_hang'):
            # Exercise the real runner deadline without waiting 15 minutes.
            # This one disposable copy changes only the internal time constant.
            assert source.count(b'ms: 15*60*1000')==1
            script.write_bytes(source.replace(b'ms: 15*60*1000',b'ms: 1000'))
        facts={**facts,**{key:secrets.token_hex(24) for key in ['build_stdout_marker','build_stderr_marker','build_private_marker']}}
        original='addr = "[::]:0"\npublic_url = "https://publication.invalid"\n'
        if facts.get('custom_root'): original+='hub_state_root = "'+str(p/'custom-root')+'"\n'
        (p/'selected.toml').write_text(original); (p/'config/evener/hub.toml').write_text(original); (p/'other.toml').write_text('addr = "127.0.0.1:9180"\n')
        (p/'alias.toml').symlink_to(p/'selected.toml')
        os.link(p/'selected.toml',p/'hardlink.toml')
        (p/'equal-content.toml').write_text(original)
        assert os.path.samefile(p/'hardlink.toml',p/'selected.toml')
        assert not os.path.samefile(p/'equal-content.toml',p/'selected.toml')
        assert (p/'equal-content.toml').read_bytes()==(p/'selected.toml').read_bytes()
        (p/'selected.toml ').write_text(original)
        (p/'space-config-alias.toml').symlink_to(p/'selected.toml ')
        assert os.path.samefile(p/'space-config-alias.toml',p/'selected.toml ')
        assert not os.path.samefile(p/'space-config-alias.toml',p/'selected.toml')
        (p/'selected.toml\n').write_text(original)
        (p/'newline-config-alias.toml').symlink_to('selected.toml\n')
        for parent in ['config-parent','config-parent\n']:
            (p/parent).mkdir(); (p/parent/'selected.toml').write_text(original)
        (p/'newline-parent-alias').symlink_to('config-parent\n',target_is_directory=True)
        assert not os.path.samefile(p/'newline-config-alias.toml',p/'selected.toml')
        assert not os.path.samefile(p/'newline-parent-alias/selected.toml',p/'config-parent/selected.toml')
        (p/'service').mkdir(); (p/'physical/inner').mkdir(parents=True); (p/'other').mkdir()
        (p/'physical ').mkdir(); (p/'space-alias').symlink_to(p/'physical ',target_is_directory=True)
        assert os.path.samefile(p/'space-alias',p/'physical ')
        assert not os.path.samefile(p/'space-alias',p/'physical')
        (p/'service-alias').symlink_to(p/'repo',target_is_directory=True)
        assert os.path.samefile(p/'service-alias',p/'repo')
        assert not os.path.samefile(p/'service-alias',p/'other')
        (p/'physical/selected.toml').write_text(original)
        (p/'link').symlink_to(p/'physical/inner')
        assert os.path.samefile(p/'alias.toml',p/'selected.toml')
        assert os.path.samefile(str(p)+'/link/../../selected.toml',p/'selected.toml')
        assert not os.path.samefile(str(p)+'/link/../selected.toml',p/'selected.toml')
        (p/'state/evener/sentinel').write_text('keep'); (p/'other-state/evener/sentinel').write_text('other-keep')
        (p/'custom-root').mkdir(); (p/'custom-root/sentinel').write_text('custom-keep')
        dispatch=p/'dispatch'; dispatch.write_text(fixture); dispatch.chmod(0o755)
        (p/'alias-bin').mkdir(); (p/'alias-bin/evener').symlink_to(p/'repo/evener')
        (p/'other/evener').write_text(fixture); (p/'other/evener').chmod(0o755)
        for tool in ['launchctl','make','curl','ps','lsof']:
            (p/'bin'/tool).symlink_to(dispatch)
        (p/'repo/evener').symlink_to(dispatch)
        assert os.path.samefile(p/'alias-bin/evener',p/'repo/evener')
        assert not os.path.samefile(p/'other/evener',p/'repo/evener')
        for tool in ['bash','python3','node','npm','git','id','awk','grep','sed','jq','stat','date','sleep','head','wc','cat','mktemp','rm','dirname','readlink']:
            executable=shutil.which(tool)
            if executable and not (tool=='node' and facts.get('no_node')) and not (tool=='npm' and facts.get('no_npm')): (p/'bin'/tool).symlink_to(executable)
        env={'PATH':str(p/'bin'),'HOME':str(p/'home'),'FIXTURE':str(p),'TMPDIR':str(p),'LC_ALL':'C','EVENER_HUB_LISTEN_ADDR':'AMBIENT_MUST_NOT_WIN','EVENER_HUB_ADDR':'REMOVED_MUST_NOT_LEAK','EVENER_HUB_URL':'https://ambient.invalid','EVENER_PROVIDERS_CONFIG':'SECRET_DO_NOT_PRINT','PRIVATE_SENTINEL':'SECRET_DO_NOT_PRINT','XDG_CONFIG_HOME':str(p/'wrong-config'),'XDG_STATE_HOME':str(p/'wrong-state')}
        if facts.get('bad_tmpdir'): env['TMPDIR']=str(p/'selected.toml')
        # FIXTURE is a command-fixture locator, not a production configuration
        # seam. Pass it through the executable wrapper, not endpoint's env.
        dispatch.write_text(fixture.replace("p = pathlib.Path(os.environ['FIXTURE'])", 'p = pathlib.Path('+repr(str(p))+')'))
        def git(*words):
            return subprocess.check_output([shutil.which('git'),*words],cwd=p/'repo',env=env,stderr=subprocess.DEVNULL,text=True).strip()
        git('init','-q'); git('config','user.name','Fixture'); git('config','user.email','fixture@invalid'); git('add','scripts/ops/deploy-hub.sh'); git('commit','-qm','fixture')
        sha=git('rev-parse','--short','HEAD')
        if facts.get('dirty'): script.write_bytes(source+b'\n# disposable tracked worktree edit\n')
        version=sha+('-dirty' if facts.get('dirty') else '')
        (p/'facts.json').write_text(json.dumps({'version':version,'sha':sha,**facts}))
        (p/'state.json').write_text(json.dumps({'pid':0 if facts.get('stopped') else 101,'other_pid':202,'built':False}))
        # Successful paths spawn many real Python command shims. Keep their
        # scheduling allowance distinct from the short negative readiness tests.
        budget='8' if success else '2'
        base=['--label','com.test.evener-selected','--timeout',budget]
        if '@raw' in args: args=[a for a in args if a!='@raw']; base=[]
        elif '@auto' in args: args=[]; base=['--timeout',budget]
        elif '@help' in args: args=['--help']; base=[]
        substitutions={'@alias':str(p/'alias.toml'),'@hardlink':str(p/'hardlink.toml'),'@equal-content':str(p/'equal-content.toml'),'@config':str(p/'selected.toml'),'@default':str(p/'config/evener/hub.toml'),'@other':str(p/'other.toml')}
        argv=['bash',str(script)]+base+[substitutions.get(a,a) for a in args]
        returncode,out,err,timed_out,leaks=invoke(argv,p,env)
        if facts.get('retry_stopped'):
            first_calls=[json.loads(line) for line in (p/'calls.jsonl').read_text().splitlines()]
            assert returncode!=0 and not timed_out and not leaks and json.loads((p/'state.json').read_text())['pid']==0
            assert sum(c['cmd']=='make' for c in first_calls)==1
            assert sum(c['cmd']=='launchctl' and c['args'][0]=='kickstart' for c in first_calls)==1
            facts['stopped_after_kick']=False
            (p/'facts.json').write_text(json.dumps({'version':version,'sha':sha,**facts}))
            returncode,more_out,more_err,timed_out,leaks=invoke(argv,p,env); out+=more_out; err+=more_err
        calls=[json.loads(line) for line in (p/'calls.jsonl').read_text().splitlines()] if (p/'calls.jsonl').exists() else []
        try:
            assert not timed_out, 'execution exceeded harness bound'
            assert not leaks, 'live command processes remained: '+str(leaks)
            assert not (p/'violation').exists(), 'undeclared command: '+(p/'violation').read_text() if (p/'violation').exists() else ''
            actual=(sum(c['cmd']=='make' for c in calls),sum(c['cmd']=='launchctl' and c['args'][0]=='kickstart' for c in calls))
            assert actual==effects, f'build/restart effects={actual}, expected={effects}'
            assert (returncode==0)==success, f'exit={returncode}, expected success={success}'
            assert not any(c['cmd']=='curl' for c in calls), 'guessed health destination probed'
            endpoints=[c for c in calls if c['cmd']=='evener' and c['args'][:3]==['hub','endpoint','--json']]
            if name.startswith('inplace_') and success:
                reports=[json.loads(line) for line in (p/'reports.jsonl').read_text().splitlines()]
                old=[r for r in reports if r['running']['pid']==101]
                new=[r for r in reports if r['running']['pid']==303]
                assert old and new, 'rewritten predecessor and original-intent successor not both observed'
                for report in old:
                    assert report['running']['requested_listen_addr']==report['running']['bound_addr']=='[::]:19471'
                    assert report['running']['listen_source']=='flag'
                for report in new:
                    assert report['running']['requested_listen_addr']==('0.0.0.0:0' if name=='inplace_environment' else '[::]:0')
                for call in endpoints:
                    if name in ['inplace_flag','inplace_space_flag']:
                        assert call['args'][-2:]==['--addr','[::]:0'], 'live bound argv replaced original launch intent'
                    else:
                        assert '--addr' not in call['args'], 'appended live listener became replacement intent'
            # Literal independent report oracles for the two corrected positives.
            # This is not another production parser or a value copied from facts.
            expected_reports={'wildcard_ipv4':('0.0.0.0:0','0.0.0.0','127.0.0.1','flag'),
                              'environment_listener':('0.0.0.0:0','0.0.0.0','127.0.0.1','env')}
            if name in expected_reports:
                requested,bound_host,local_host,listen_source=expected_reports[name]
                reports=[json.loads(line) for line in (p/'reports.jsonl').read_text().splitlines()]
                assert len(reports)>=3
                for report in reports:
                    running=report['running']; port=running['bound_addr'].rsplit(':',1)[1]
                    assert report['configured_listen_addr']==running['requested_listen_addr']==requested
                    assert report['listen_source']==running['listen_source']==listen_source
                    assert running['bound_addr']==bound_host+':'+port and port!='0'
                    assert running['local_url']==running['loopback_url']=='http://'+local_host+':'+port
            if name=='intentional_toml_edit':
                reports=[json.loads(line) for line in (p/'reports.jsonl').read_text().splitlines()]
                assert any(r['configured_listen_addr']=='0.0.0.0:0' and r['running']['requested_listen_addr']=='[::]:0' for r in reports), 'edited config not observed before restart'
                successors=[r for r in reports if r['running']['pid']==303]
                assert successors, 'edited listener successor not observed'
                for report in successors:
                    running=report['running']; port=running['bound_addr'].rsplit(':',1)[1]
                    assert report['configured_listen_addr']==running['requested_listen_addr']=='0.0.0.0:0'
                    assert report['listen_source']==running['listen_source']=='config'
                    assert running['bound_addr']=='0.0.0.0:'+port and port!='0'
                    assert running['local_url']==running['loopback_url']=='http://127.0.0.1:'+port
                    assert 'public_url' not in report
            if name=='stopped_cwd_space_collision':
                kick=next(i for i,c in enumerate(calls) if c['cmd']=='launchctl' and c['args'][0]=='kickstart')
                assert len(endpoints)==2 and not any(c in endpoints for c in calls[kick:]), 'wrong-cwd successor reached endpoint inspection'
            elif effects==(1,1):
                assert len(endpoints)>=3, 'selected endpoint not re-resolved before/after restart'
                build=next(i for i,c in enumerate(calls) if c['cmd']=='make')
                assert any(c in endpoints for c in calls[:build]), 'build preceded endpoint authorization'
            for call in endpoints:
                expected_env={'HOME':str(p/'home'),'XDG_CONFIG_HOME':str(p/'config'),'XDG_STATE_HOME':str(p/'state')}
                if facts.get('env_listener') or facts.get('flag_and_env'): expected_env['EVENER_HUB_LISTEN_ADDR']='0.0.0.0:0'
                if facts.get('empty_listener'): expected_env['EVENER_HUB_LISTEN_ADDR']=''
                if facts.get('definition_change') and call['env'].get('XDG_STATE_HOME')==str(p/'other-state'): expected_env['XDG_STATE_HOME']=str(p/'other-state')
                assert call['env']==expected_env, 'ambient/environment contamination: '+str(call['env'])
                expected_cwd=p/'physical ' if facts.get('cwd_space_alias') or facts.get('cwd_space_literal') else (p/'service' if facts.get('service_cwd') else p/'repo')
                assert call['cwd']==str(expected_cwd), 'wrong service working directory'
            assert b'SECRET_DO_NOT_PRINT' not in out+err, 'secret disclosed'
            markers=[facts[key].encode() for key in ['build_stdout_marker','build_stderr_marker','build_private_marker']]
            assert all(marker not in out+err for marker in markers), 'raw build output disclosed'
            artifacts=list(p.glob('evener-deploy-build-*'))
            failed_build=any(facts.get(key) for key in ['build_fail','build_stdout_large','build_stderr_large','build_hang'])
            if failed_build:
                assert len(artifacts)==1, 'private build evidence was not retained'
                directory=artifacts[0]; diagnostic=directory/'build.log'
                assert stat.S_IMODE(directory.stat().st_mode)==0o700 and directory.stat().st_uid==os.getuid(), 'diagnostic directory is not owner-only'
                assert diagnostic.is_file() and not diagnostic.is_symlink(), 'diagnostic file missing or redirected'
                assert stat.S_IMODE(diagnostic.stat().st_mode)==0o600 and diagnostic.stat().st_uid==os.getuid(), 'diagnostic file is not owner-only'
                assert str(diagnostic).encode() in out+err, 'retained diagnostic path not named to operator'
                evidence=diagnostic.read_bytes()
                # At the cap, only the flooding stream's prefix is guaranteed
                # to precede truncation, not data waiting in the other pipe.
                retained=([markers[0],markers[2]] if facts.get('build_stdout_large') else
                          [markers[1],markers[2]] if facts.get('build_stderr_large') else markers)
                assert all(marker in evidence for marker in retained), 'opaque build diagnosis was discarded'
                assert b'SECRET_DO_NOT_PRINT' not in evidence, 'service/environment inspection leaked into build evidence'
                assert 0<len(evidence)<=8*1024*1024, 'diagnostic size exceeds combined build cap'
                if facts.get('build_stdout_large') or facts.get('build_stderr_large'):
                    assert len(evidence)==8*1024*1024, 'bounded build prefix was lost'
            else:
                assert not artifacts, 'successful or unattempted build left diagnostic files'
            assert not (p/'executed').exists(), 'unvalidated shell output executed'
            assert (p/'state/evener/sentinel').read_text()=='keep'
            assert (p/'other-state/evener/sentinel').read_text()=='other-keep'
            assert (p/'custom-root/sentinel').read_text()=='custom-keep'
            state=json.loads((p/'state.json').read_text()); assert state['other_pid']==202, 'unselected service changed'
            if success and effects[1]>0: assert state['pid']==303
            if facts.get('port_refresh'): assert state['successor_queries']>=2, 'successor endpoint not rediscovered'
            assert (p/'selected.toml').read_text()==('addr = "0.0.0.0:0"\n' if facts.get('toml_edit') else original), 'listener/config was rewritten'
            print('PASS '+name)
        except AssertionError as e: failures.append((name,str(e),out,err))
for name,reason,out,err in failures:
    print('FAIL '+name+': '+reason)
    print((out+err).decode(errors='replace')[:4000])
if failures: sys.exit(1)
print(str(len(cases))+' actual-script command-boundary cases passed, no macOS services executed.')
PY
