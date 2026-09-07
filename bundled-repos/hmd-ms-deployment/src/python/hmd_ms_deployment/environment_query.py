"""
SQL scripts to load all objects related to a single environment
"""

base = """with undeleted as (
    select id, name, content, created_at, updated_at from entity
    where is_deleted = FALSE
), undel_rels as (
    select id, name, content, created_at, updated_at, from_id, to_id from relationship
    where is_deleted = FALSE
), environment as (
    select id, name, content, created_at, updated_at from undeleted as env
    where
        env.name = 'hmd_lang_deployment.environment'
        and env.content ->> 'type'::text = '{environment_type}'
), ri as (
    select ri.id, ri.name, ri.content, ri.created_at, ri.updated_at from undeleted as ri
    join undel_rels ehri on ehri.name = 'hmd_lang_deployment.environment_has_repo_instance'
        and ehri.to_id = ri.id
    join environment as env on env.id = ehri.from_id
), rc as (
    select rc.id, rc.name, rc.content, rc.created_at, rc.updated_at from undeleted as rc
    join undel_rels riirc on riirc.name = 'hmd_lang_deployment.repo_instance_isa_repo_class'
        and riirc.to_id = rc.id
    join ri on ri.id = riirc.from_id
), rid as (
    select rid.id, rid.name, rid.content, rid.created_at, rid.updated_at from undeleted as rid
    join undel_rels rihrid on rihrid.name = 'hmd_lang_deployment.repo_instance_has_repo_instance_deployment'
        and rihrid.to_id = rid.id
    join ri on ri.id = rihrid.from_id
    where rihrid.content ->> 'current'::text = 'true' or rid.content ->> 'status'::text in ('DEPLOY_NEXT', 'DESTROY_NEXT')
), rcv as (
    select rcv.id, rcv.name, rcv.content, rcv.created_at, rcv.updated_at from undeleted as rcv
    join undel_rels ridhrcv on ridhrcv.name = 'hmd_lang_deployment.repo_instance_deployment_has_repo_class_version'
        and ridhrcv.to_id = rcv.id
    join rid on rid.id = ridhrcv.from_id
)"""

nouns = (
    base
    + """
select * from rcv
union all
select * from rid
union all 
select * from rc
union all 
select * from ri
union all 
select * from environment
"""
)

relationships = (
    base
    + """
select ur.* from undel_rels as ur
join environment a on a.id = ur.from_id
join ri b on b.id = ur.to_id
where ur.name = 'hmd_lang_deployment.environment_has_repo_instance'
union all
select ur.* from undel_rels as ur
join ri a on a.id = ur.from_id
join ri b on b.id = ur.to_id
where ur.name = 'hmd_lang_deployment.repo_instance_req_repo_instance'
union all
select ur.* from undel_rels as ur
join ri a on a.id = ur.from_id
join rc b on b.id = ur.to_id
where ur.name = 'hmd_lang_deployment.repo_instance_isa_repo_class'
union all
select ur.* from undel_rels as ur
join ri a on a.id = ur.from_id
join rid b on b.id = ur.to_id
where ur.name = 'hmd_lang_deployment.repo_instance_has_repo_instance_deployment'
union all
select ur.* from undel_rels as ur
join rid a on a.id = ur.from_id
join rcv b on b.id = ur.to_id
where ur.name = 'hmd_lang_deployment.repo_instance_deployment_has_repo_class_version'
union all
select ur.* from undel_rels as ur
join rc a on a.id = ur.from_id
join rcv b on b.id = ur.to_id
where ur.name = 'hmd_lang_deployment.repo_class_has_repo_class_version'
"""
)
