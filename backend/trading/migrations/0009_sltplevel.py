from django.db import migrations, models
import django.db.models.deletion


class Migration(migrations.Migration):

    dependencies = [
        ('trading', '0008_sltp_fields'),
    ]

    operations = [
        migrations.CreateModel(
            name='SLTPLevel',
            fields=[
                ('id', models.BigAutoField(auto_created=True, primary_key=True, serialize=False, verbose_name='ID')),
                ('level_type', models.CharField(choices=[('SL', 'Stop Loss'), ('TP', 'Take Profit')], max_length=2)),
                ('price', models.DecimalField(decimal_places=4, max_digits=15)),
                ('quantity', models.DecimalField(decimal_places=4, max_digits=15)),
                ('triggered', models.BooleanField(default=False)),
                ('triggered_at', models.DateTimeField(blank=True, null=True)),
                ('created_at', models.DateTimeField(auto_now_add=True)),
                ('position', models.ForeignKey(
                    blank=True, null=True,
                    on_delete=django.db.models.deletion.CASCADE,
                    related_name='sltp_levels',
                    to='trading.position',
                )),
                ('cfd_position', models.ForeignKey(
                    blank=True, null=True,
                    on_delete=django.db.models.deletion.CASCADE,
                    related_name='sltp_levels',
                    to='trading.cfdposition',
                )),
            ],
            options={
                'ordering': ['level_type', 'price'],
            },
        ),
    ]
