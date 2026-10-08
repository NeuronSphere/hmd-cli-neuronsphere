
drop view if exists transform_has_transform_version_hmd_lang_transform;
create view transform_has_transform_version_hmd_lang_transform as
    select
        id,
        from_id,
        to_id,
        created_at, updated_at
    from relationship
    where is_deleted = false and name = 'hmd_lang_transform.transform_has_transform_version';

